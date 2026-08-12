package row

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

// plannedField is one struct field a decodePlan maps to a result column, in
// the row type's declaration order.
type plannedField struct {
	index  int
	column string
}

// decodePlan holds everything Decode recomputes per row today: whether the row
// type maps itself through DecodeRow, and if not, which fields map to which
// columns. Building one requires walking the type's fields and, for a type
// that implements Decoder, resolving embedded-Decoder shadowing, so a plan is
// built once per type and cached rather than redone on every row.
type decodePlan struct {
	// err is the single per-type error when rowType cannot be decoded at all:
	// a build failure from fieldsShadowEmbeddedDecoder, an unexported tagged
	// field, or an empty rasql column name. A type whose plan holds an error
	// reports the same error on every Decode call.
	err error
	// errWraps is true when err came from fieldsShadowEmbeddedDecoder, which
	// Decode wraps as "row: decode %T: %w". An err from the field walk (an
	// unexported tagged field or an empty rasql tag) is already a complete
	// message and Decode returns it unwrapped, matching today's two wrapping
	// sites exactly.
	errWraps bool
	// mapsItself is true when *rowType implements Decoder and its fields do
	// not shadow the embedded Decoder, so Decode calls DecodeRow instead of
	// reading fields.
	mapsItself bool
	// isStruct is true when rowType's kind is struct. A type that does not
	// map itself and is not a struct cannot be field-mapped.
	isStruct bool
	// fields lists the mapped fields in declaration order, valid only when
	// mapsItself is false and isStruct is true.
	fields []plannedField
}

// decodePlans caches one *decodePlan per row type. The cache grows with the
// number of distinct row types decoded, which normal use bounds: a program
// decodes a fixed set of row types declared in its source. A program that
// manufactures row types at run time with reflect.StructOf could grow this
// cache without bound; encoding/json carries the same risk with the same
// structure, caching its own per-type encoders and decoders the same way.
var decodePlans sync.Map // reflect.Type -> *decodePlan

// decodePlanBuilds counts how many times buildPlan has run. It exists only so
// a test can prove a type's plan is built once no matter how many rows of
// that type are decoded; nothing in this package reads it otherwise.
var decodePlanBuilds atomic.Int64

// planFor returns the cached decodePlan for rowType, building and storing one
// on first use. LoadOrStore, not Store, is used so that when two goroutines
// race to build the same type's plan, both builds complete but only one is
// kept, and every caller ends up with the same *decodePlan.
func planFor(rowType reflect.Type) *decodePlan {
	if cached, ok := decodePlans.Load(rowType); ok {
		return cached.(*decodePlan)
	}
	built := buildPlan(rowType)
	actual, _ := decodePlans.LoadOrStore(rowType, built)
	return actual.(*decodePlan)
}

// buildPlan reproduces, once per type, the logic Decode used to redo on every
// row: deciding whether *rowType maps itself through Decoder, and otherwise
// walking its fields to resolve each one's column name.
func buildPlan(rowType reflect.Type) *decodePlan {
	decodePlanBuilds.Add(1)

	plan := &decodePlan{isStruct: rowType.Kind() == reflect.Struct}

	// reflect.PointerTo(rowType).Implements(decoderType) is exactly equivalent
	// to today's any(&result).(Decoder) type assertion for every case Decode
	// sees: a pointer method set contains a value-receiver DecodeRow as well as
	// a pointer-receiver one, so a value-receiver method is found the same way;
	// rowType may itself be a pointer type, whose own pointer type still carries
	// whatever *rowType would, so the check still matches interface assertion
	// against a *rowType value; and rowType may be an interface type, where
	// PointerTo(rowType) is a pointer-to-interface that implements Decoder
	// exactly when the interface itself embeds Decoder, matching what asserting
	// a *interface{} value's dynamic type would require. plan_internal_test.go
	// checks this equivalence directly across all these shapes.
	if reflect.PointerTo(rowType).Implements(decoderType) {
		shadowed, err := fieldsShadowEmbeddedDecoder(rowType)
		if err != nil {
			plan.err = err
			plan.errWraps = true
			return plan
		}
		plan.mapsItself = !shadowed
		if plan.mapsItself {
			return plan
		}
	}

	if !plan.isStruct {
		return plan
	}

	fields := make([]plannedField, 0, rowType.NumField())
	for index := range rowType.NumField() {
		field := rowType.Field(index)
		columnName, tagged := field.Tag.Lookup("rasql")
		if columnName == "-" {
			continue
		}
		if field.PkgPath != "" {
			if tagged {
				plan.err = fmt.Errorf("row: field %s for column %q is not exported", field.Name, columnName)
				return plan
			}
			continue
		}
		if tagged {
			columnName, _, _ = strings.Cut(columnName, ",")
			if columnName == "" {
				plan.err = fmt.Errorf("row: field %s has an empty rasql column name", field.Name)
				return plan
			}
		} else {
			columnName = snakeCase(field.Name)
		}
		fields = append(fields, plannedField{index: index, column: columnName})
	}
	plan.fields = fields
	return plan
}
