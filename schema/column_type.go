package schema

import (
	"encoding/json"
	"fmt"
)

// TypeKind identifies the family of a column type.
type TypeKind string

const (
	KindBoolean TypeKind = "boolean"
	KindInteger TypeKind = "integer"
	KindFloat   TypeKind = "float"
	KindText    TypeKind = "text"
	KindBytes   TypeKind = "bytes"
	KindTime    TypeKind = "time"
	KindJSON    TypeKind = "json"
	KindUUID    TypeKind = "uuid"
	KindDecimal TypeKind = "decimal"
)

// ColumnType describes the type-specific part of a column. The unexported
// method keeps the set of built-in types closed so dialects and generators
// can handle every supported type explicitly.
type ColumnType interface {
	Kind() TypeKind
	columnType()
}

// BooleanType describes a boolean column.
type BooleanType struct{}

func (BooleanType) Kind() TypeKind { return KindBoolean }
func (BooleanType) columnType()    {}

// IntegerType describes an integer column.
//
// DisplayWidth and ZeroFill are both MySQL-only facts. DisplayWidth records a
// stated display width such as the 11 in int(11); see IntegerDisplayWidth's
// own doc for what its zero value means. ZeroFill records MySQL's ZEROFILL
// attribute, which left-pads a displayed value with zeros up to its display
// width and implies UNSIGNED; its zero value, false, means the column carries
// no ZEROFILL, which is what every IntegerType and every checked-in
// generated file written before this field existed has always meant.
//
// A stated DisplayWidth or a true ZeroFill is describable but not yet
// renderable: inspect records what a live MySQL integer column actually
// declares, and TableDef.Validate accepts it, but render.CreateTable and the
// migrate diff-live path refuse to build DDL for one, because rasql does not
// yet know how to construct an INT(n) or ZEROFILL declaration.
type IntegerType struct {
	Unsigned     bool
	DisplayWidth IntegerDisplayWidth
	ZeroFill     bool
}

func (IntegerType) Kind() TypeKind { return KindInteger }
func (IntegerType) columnType()    {}

// FloatType describes a floating-point column.
type FloatType struct{}

func (FloatType) Kind() TypeKind { return KindFloat }
func (FloatType) columnType()    {}

// TextType describes a text column. Width is the maximum number of
// characters it may store; its zero value means no width was stated, which
// is the same unbounded column TextType always described before Width
// existed. A dialect that cannot index, or build a primary key or unique
// constraint over, an unbounded text column refuses to render one used that
// way rather than send the database a statement it will reject; see
// dialect.Dialect.TypeName and render.CreateTable.
//
// Fixed reports whether the column is fixed-width (CHAR(n) rather than
// VARCHAR(n)) on a dialect that distinguishes the two. It is meaningless
// unless Width states a value: bare CHAR means CHAR(1), not an unbounded
// column, so Table.Validate refuses Fixed set without a stated Width. Use
// the Fixed ColumnOption, alongside Width, to state it.
type TextType struct {
	Width TextWidth
	Fixed bool
}

func (TextType) Kind() TypeKind { return KindText }
func (TextType) columnType()    {}

// BytesType describes a binary column.
type BytesType struct{}

func (BytesType) Kind() TypeKind { return KindBytes }
func (BytesType) columnType()    {}

// TimeType describes a time column.
type TimeType struct{}

func (TimeType) Kind() TypeKind { return KindTime }
func (TimeType) columnType()    {}

// JSONType describes a JSON column.
type JSONType struct{}

func (JSONType) Kind() TypeKind { return KindJSON }
func (JSONType) columnType()    {}

// UUIDType describes a UUID column.
type UUIDType struct{}

func (UUIDType) Kind() TypeKind { return KindUUID }
func (UUIDType) columnType()    {}

// DecimalType describes an exact decimal column.
type DecimalType struct {
	Precision int
	Scale     DecimalScale
}

func (DecimalType) Kind() TypeKind { return KindDecimal }
func (DecimalType) columnType()    {}

func validColumnType(columnType ColumnType) bool {
	switch columnType.(type) {
	case BooleanType, IntegerType, FloatType, TextType, BytesType, TimeType, JSONType, UUIDType, DecimalType:
		return true
	default:
		return false
	}
}

func marshalColumnType(columnType ColumnType) ([]byte, error) {
	if !validColumnType(columnType) {
		return nil, fmt.Errorf("schema: cannot encode unsupported column type %T", columnType)
	}

	fields := map[string]any{"Kind": columnType.Kind()}
	switch typed := columnType.(type) {
	case IntegerType:
		fields["Unsigned"] = typed.Unsigned
		width, stated := typed.DisplayWidth.Value()
		if stated {
			fields["DisplayWidth"] = width
		} else {
			fields["DisplayWidth"] = nil
		}
		fields["ZeroFill"] = typed.ZeroFill
	case TextType:
		width, stated := typed.Width.Value()
		if stated {
			fields["Width"] = width
		} else {
			fields["Width"] = nil
		}
		fields["Fixed"] = typed.Fixed
	case DecimalType:
		fields["Precision"] = typed.Precision
		scale, stated := typed.Scale.Value()
		if stated {
			fields["Scale"] = scale
		} else {
			fields["Scale"] = nil
		}
	}
	return json.Marshal(fields)
}

func unmarshalColumnType(data []byte) (ColumnType, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("schema: decode column type: %w", err)
	}
	kindData, ok := fields["Kind"]
	if !ok {
		return nil, fmt.Errorf("schema: decode column type: missing Kind")
	}
	var kind TypeKind
	if err := json.Unmarshal(kindData, &kind); err != nil {
		return nil, fmt.Errorf("schema: decode column type kind: %w", err)
	}
	delete(fields, "Kind")

	allow := func(names ...string) error {
		allowed := make(map[string]struct{}, len(names))
		for _, name := range names {
			allowed[name] = struct{}{}
		}
		for name := range fields {
			if _, ok := allowed[name]; !ok {
				return fmt.Errorf("schema: column type %q does not accept %q", kind, name)
			}
		}
		return nil
	}

	switch kind {
	case KindBoolean:
		if err := allow(); err != nil {
			return nil, err
		}
		return BooleanType{}, nil
	case KindInteger:
		if err := allow("Unsigned", "DisplayWidth", "ZeroFill"); err != nil {
			return nil, err
		}
		var unsigned bool
		if value, ok := fields["Unsigned"]; ok {
			if err := json.Unmarshal(value, &unsigned); err != nil {
				return nil, fmt.Errorf("schema: decode integer unsigned: %w", err)
			}
		}
		var displayWidth IntegerDisplayWidth
		if value, ok := fields["DisplayWidth"]; ok {
			if err := json.Unmarshal(value, &displayWidth); err != nil {
				return nil, fmt.Errorf("schema: decode integer display width: %w", err)
			}
		}
		var zeroFill bool
		if value, ok := fields["ZeroFill"]; ok {
			if err := json.Unmarshal(value, &zeroFill); err != nil {
				return nil, fmt.Errorf("schema: decode integer zerofill: %w", err)
			}
		}
		return IntegerType{Unsigned: unsigned, DisplayWidth: displayWidth, ZeroFill: zeroFill}, nil
	case KindFloat:
		if err := allow(); err != nil {
			return nil, err
		}
		return FloatType{}, nil
	case KindText:
		if err := allow("Width", "Fixed"); err != nil {
			return nil, err
		}
		var width TextWidth
		if value, ok := fields["Width"]; ok {
			if err := json.Unmarshal(value, &width); err != nil {
				return nil, fmt.Errorf("schema: decode text width: %w", err)
			}
		}
		var fixed bool
		if value, ok := fields["Fixed"]; ok {
			if err := json.Unmarshal(value, &fixed); err != nil {
				return nil, fmt.Errorf("schema: decode text fixed: %w", err)
			}
		}
		return TextType{Width: width, Fixed: fixed}, nil
	case KindBytes:
		if err := allow(); err != nil {
			return nil, err
		}
		return BytesType{}, nil
	case KindTime:
		if err := allow(); err != nil {
			return nil, err
		}
		return TimeType{}, nil
	case KindJSON:
		if err := allow(); err != nil {
			return nil, err
		}
		return JSONType{}, nil
	case KindUUID:
		if err := allow(); err != nil {
			return nil, err
		}
		return UUIDType{}, nil
	case KindDecimal:
		if err := allow("Precision", "Scale"); err != nil {
			return nil, err
		}
		var precision int
		if value, ok := fields["Precision"]; ok {
			if err := json.Unmarshal(value, &precision); err != nil {
				return nil, fmt.Errorf("schema: decode decimal precision: %w", err)
			}
		}
		var scale DecimalScale
		if value, ok := fields["Scale"]; ok {
			if err := json.Unmarshal(value, &scale); err != nil {
				return nil, fmt.Errorf("schema: decode decimal scale: %w", err)
			}
		}
		return DecimalType{Precision: precision, Scale: scale}, nil
	default:
		return nil, fmt.Errorf("schema: decode column type: unsupported kind %q", kind)
	}
}
