package rowvalue

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

// decodePlan holds everything Decode recomputes per row today: which fields
// map to which columns. Building one requires walking the type's fields, so a
// plan is built once per type and cached rather than redone on every row.
type decodePlan struct {
	// err is the single per-type error when rowType cannot be decoded at all:
	// an unexported tagged field, or an empty rasql column name. A type whose
	// plan holds an error reports the same error on every Decode call.
	err error
	// isStruct is true when rowType's kind is struct. A type that is not a
	// struct cannot be field-mapped.
	isStruct bool
	// fields lists the mapped fields in declaration order, valid only when
	// isStruct is true.
	fields []plannedField
}

// Decoder prepares the cached reflective mapping for one result type and can
// validate known result columns before any rows are read.
type Decoder[T any] struct {
	plan *decodePlan
}

// NewDecoder prepares the reflective decoder for T.
func NewDecoder[T any]() (Decoder[T], error) {
	var result T
	plan := planFor(reflect.TypeFor[T]())
	if plan.err != nil {
		return Decoder[T]{}, plan.err
	}
	if !plan.isStruct {
		return Decoder[T]{}, fmt.Errorf("row: decode destination %T must be a struct", result)
	}
	if len(plan.fields) == 0 {
		return Decoder[T]{}, fmt.Errorf("row: decode destination %T has no exported fields", result)
	}
	return Decoder[T]{plan: plan}, nil
}

// ValidateColumns checks that every planned field has a known result column.
// Extra, duplicate, and empty names are left to runtime row validation.
func (d Decoder[T]) ValidateColumns(names []string) error {
	columns := make(map[string]struct{}, len(names))
	for _, name := range names {
		columns[name] = struct{}{}
	}
	for _, field := range d.plan.fields {
		if _, ok := columns[field.column]; !ok {
			return fmt.Errorf("row: column %q is not present", field.column)
		}
	}
	return nil
}

// Decode applies the prepared mapping to one row.
func (d Decoder[T]) Decode(r Row) (T, error) {
	var result T
	destination := reflect.ValueOf(&result).Elem()
	for _, field := range d.plan.fields {
		value, ok := r.lookup(field.column)
		if !ok {
			return result, fmt.Errorf("row: column %q is not present", field.column)
		}
		if err := assign(destination.Field(field.index), value); err != nil {
			return result, fmt.Errorf("row: decode column %q: %w", field.column, err)
		}
	}
	return result, nil
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
// row: walking rowType's fields to resolve each one's column name.
func buildPlan(rowType reflect.Type) *decodePlan {
	decodePlanBuilds.Add(1)

	plan := &decodePlan{isStruct: rowType.Kind() == reflect.Struct}
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
