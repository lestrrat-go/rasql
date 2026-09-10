package rasql

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type dynamicField struct {
	index  []int
	column string
}

type dynamicDecoder[R any] struct {
	schema ResultSchema
	fields []dynamicField
}

func DynamicProjection[R any](result ResultSchema) (Projection[R], error) {
	columns := result.Columns()
	if len(columns) == 0 {
		return Projection[R]{}, planError("invalid_projection", "schema", "must not be empty")
	}
	fields, err := dynamicFields(reflect.TypeFor[R]())
	if err != nil {
		return Projection[R]{}, planError("invalid_projection", "decoder", err.Error())
	}
	byName := make(map[string]dynamicField, len(fields))
	for _, field := range fields {
		if _, ok := byName[field.column]; ok {
			return Projection[R]{}, planError("invalid_projection", "decoder", "duplicate field mapping for "+field.column)
		}
		byName[field.column] = field
	}
	for i, column := range columns {
		field, ok := byName[column.Name]
		if !ok {
			return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("schema.columns[%d]", i), "no Go field maps to "+column.Name)
		}
		if err := compatibleDynamicField(reflect.TypeFor[R](), field.index, column); err != nil {
			return Projection[R]{}, planError("invalid_projection", fmt.Sprintf("schema.columns[%d]", i), err.Error())
		}
	}
	orderedFields := make([]dynamicField, len(columns))
	for i, column := range columns {
		for _, field := range fields {
			if field.column == column.Name {
				orderedFields[i] = field
				break
			}
		}
	}
	decoder := &dynamicDecoder[R]{schema: result, fields: orderedFields}
	items := make([]ProjectionItem, len(columns))
	for i, column := range columns {
		items[i] = ProjectionItem{expression: query.Bind(nil), column: column}
	}
	return NewProjection(items, decoder)
}

func dynamicFields(typ reflect.Type) ([]dynamicField, error) {
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("decode destination %s must be a struct", typ)
	}
	var fields []dynamicField
	var walk func(reflect.Type, []int) error
	walk = func(current reflect.Type, prefix []int) error {
		for i := 0; i < current.NumField(); i++ {
			field := current.Field(i)
			index := append(append([]int(nil), prefix...), i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Tag.Get("rasql") == "" && field.Tag.Get("json") == "" {
				if err := walk(field.Type, index); err != nil {
					return err
				}
				continue
			}
			if field.PkgPath != "" && field.Tag.Get("rasql") == "" && field.Tag.Get("json") == "" {
				continue
			}
			if field.PkgPath != "" {
				return fmt.Errorf("field %s is unexported", field.Name)
			}
			name := field.Tag.Get("rasql")
			if name != "" {
				name = strings.Split(name, ",")[0]
			}
			if name == "" {
				name = strings.Split(field.Tag.Get("json"), ",")[0]
			}
			if name == "" || name == "-" {
				if name == "-" {
					continue
				}
				name = snakeDynamic(field.Name)
			}
			fields = append(fields, dynamicField{index: index, column: name})
		}
		return nil
	}
	if err := walk(typ, nil); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("decode destination %s has no exported fields", typ)
	}
	return fields, nil
}

func snakeDynamic(name string) string {
	runes := []rune(name)
	var out strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) ||
			(unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

func compatibleDynamicField(root reflect.Type, index []int, column ResultColumn) error {
	typ := root
	for _, i := range index {
		typ = typ.Field(i).Type
	}
	base := typ
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	scanner := reflect.PointerTo(base).Implements(reflect.TypeFor[sql.Scanner]()) || base.Implements(reflect.TypeFor[sql.Scanner]())
	nullableStruct := reflect.PointerTo(base).Implements(reflect.TypeFor[nullableScanDestination]())
	if column.Nullable {
		if typ.Kind() != reflect.Pointer && typ.Kind() != reflect.Interface && typ.Kind() != reflect.Slice && typ.Kind() != reflect.Map && !scanner && !nullableStruct {
			return fmt.Errorf("nullable column %q requires nullable Go field", column.Name)
		}
	}
	if scanner {
		return nil
	}
	if nullableStruct {
		return nil
	}
	if base == reflect.TypeFor[time.Time]() {
		return nil
	}
	compatible := false
	switch column.Type.Kind() {
	case schema.KindBoolean:
		compatible = base.Kind() == reflect.Bool
	case schema.KindInteger:
		compatible = base.Kind() >= reflect.Int && base.Kind() <= reflect.Int64 || base.Kind() >= reflect.Uint && base.Kind() <= reflect.Uint64
	case schema.KindFloat, schema.KindDecimal:
		compatible = base.Kind() == reflect.Float32 || base.Kind() == reflect.Float64
	case schema.KindText, schema.KindUUID:
		compatible = base.Kind() == reflect.String || (base.Kind() == reflect.Slice && base.Elem().Kind() == reflect.Uint8)
	case schema.KindBytes, schema.KindJSON:
		compatible = base.Kind() == reflect.String || (base.Kind() == reflect.Slice && base.Elem().Kind() == reflect.Uint8) || base.Kind() == reflect.Interface
	default:
		compatible = true
	}
	if !compatible {
		return fmt.Errorf("go field for %q is incompatible with %s", column.Name, column.Type.Kind())
	}
	return nil
}

func (d *dynamicDecoder[R]) ResultSchema() ResultSchema { return d.schema }
func (d *dynamicDecoder[R]) Presence() []Presence       { return nil }
func (d *dynamicDecoder[R]) DecodeRow(source ScanSource, result *R) error {
	values := make([]any, len(d.fields))
	for i := range d.fields {
		field, err := fieldValue(reflect.ValueOf(result).Elem(), d.fields[i].index)
		if err != nil {
			return err
		}
		values[i] = field.Addr().Interface()
	}
	if err := source.Scan(values...); err != nil {
		return err
	}
	return nil
}

func fieldValue(value reflect.Value, index []int) (reflect.Value, error) {
	for _, i := range index {
		if value.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("field path is not a struct")
		}
		value = value.Field(i)
	}
	if !value.CanAddr() || !value.CanSet() {
		return reflect.Value{}, fmt.Errorf("field is not settable")
	}
	return value, nil
}

func (d *dynamicDecoder[R]) bindReturnedColumns(names []string) (RowDecoder[R], ResultSchema, error) {
	declared := d.schema.Columns()
	if len(names) != len(declared) {
		return nil, ResultSchema{}, planError("uncertain_contract", "result.columns", "returned column count differs")
	}
	byName := make(map[string]ResultColumn, len(declared))
	fields := make(map[string]dynamicField, len(d.fields))
	for _, column := range declared {
		byName[column.Name] = column
	}
	for _, field := range d.fields {
		fields[field.column] = field
	}
	ordered := make([]ResultColumn, len(names))
	seen := make(map[string]struct{}, len(names))
	orderedFields := make([]dynamicField, len(names))
	for i, name := range names {
		if name == "" {
			return nil, ResultSchema{}, planError("uncertain_contract", "result.columns", "returned column is empty")
		}
		if _, ok := seen[name]; ok {
			return nil, ResultSchema{}, planError("uncertain_contract", "result.columns", "returned column is duplicated")
		}
		seen[name] = struct{}{}
		column, ok := byName[name]
		field, fieldOK := fields[name]
		if !ok || !fieldOK {
			return nil, ResultSchema{}, planError("uncertain_contract", "result.columns", "returned column is unknown")
		}
		ordered[i], orderedFields[i] = column, field
	}
	boundSchema, err := NewResultSchema(ordered...)
	if err != nil {
		return nil, ResultSchema{}, err
	}
	return &dynamicDecoder[R]{schema: boundSchema, fields: orderedFields}, boundSchema, nil
}

var _ returnedColumnBinder[struct{}] = (*dynamicDecoder[struct{}])(nil)
var _ = (*dynamicDecoder[any]).bindReturnedColumns
