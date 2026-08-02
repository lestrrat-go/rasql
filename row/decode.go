package row

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/lestrrat-go/rasql/internal/method"
)

var timeType = reflect.TypeFor[time.Time]()

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// Decoder is implemented by row types that map result columns themselves.
// Decode prefers it over struct tags and snake-cased field names, so a
// generated row type carries its own mapping instead of restating it as a tag.
// A single result value is decoded by ColumnDecoder instead.
//
// One shape is mapped by its fields even though it satisfies Decoder: a struct
// that embeds a Decoder, declares mappable fields of its own, and declares no
// DecodeRow. Go promotes the embedded DecodeRow to the outer struct, where it
// knows nothing about the fields declared around it, so those fields win.
// Declaring DecodeRow on the outer type maps such a struct by method again.
type Decoder interface {
	DecodeRow(Row) error
}

// Get decodes the named value in r as T.
func Get[T any](r Row, name string) (T, error) {
	var result T
	if err := Assign(r, name, &result); err != nil {
		return result, err
	}
	return result, nil
}

// Assign decodes the named value in r into destination.
func Assign[T any](r Row, name string, destination *T) error {
	if destination == nil {
		return fmt.Errorf("row: destination for column %q must not be nil", name)
	}
	value, ok := r.values[name]
	if !ok {
		return fmt.Errorf("row: column %q is not present", name)
	}
	if err := assign(reflect.ValueOf(destination).Elem(), value); err != nil {
		return fmt.Errorf("row: decode column %q: %w", name, err)
	}
	return nil
}

// Decode populates T through its DecodeRow method when it declares one, and from
// rasql-tagged fields or snake-cased exported field names otherwise. Decoder
// states which of the two a struct that embeds a Decoder takes.
func Decode[T any](r Row) (T, error) {
	var result T
	// A row type that states its own mapping needs no fields to map, so its
	// DecodeRow is called and nothing is read by reflection. A DecodeRow promoted
	// from an embedded field is not that statement when the row type declares
	// mappable fields of its own, so those fields are read instead. A DecodeRow
	// the row type declares itself is always that statement.
	if decoder, ok := any(&result).(Decoder); ok {
		shadowed, err := fieldsShadowEmbeddedDecoder(reflect.TypeFor[T]())
		if err != nil {
			return result, fmt.Errorf("row: decode %T: %w", result, err)
		}
		if !shadowed {
			if err := decoder.DecodeRow(r); err != nil {
				return result, fmt.Errorf("row: decode %T: %w", result, err)
			}
			return result, nil
		}
	}

	destination := reflect.ValueOf(&result).Elem()
	if destination.Kind() != reflect.Struct {
		return result, fmt.Errorf("row: decode destination %T must be a struct", result)
	}

	fields := 0
	typeOfResult := destination.Type()
	for index := range destination.NumField() {
		field := typeOfResult.Field(index)
		columnName, tagged := field.Tag.Lookup("rasql")
		if columnName == "-" {
			continue
		}
		if field.PkgPath != "" {
			if tagged {
				return result, fmt.Errorf("row: field %s for column %q is not exported", field.Name, columnName)
			}
			continue
		}
		if tagged {
			columnName, _, _ = strings.Cut(columnName, ",")
			if columnName == "" {
				return result, fmt.Errorf("row: field %s has an empty rasql column name", field.Name)
			}
		} else {
			columnName = snakeCase(field.Name)
		}
		fields++
		value, ok := r.values[columnName]
		if !ok {
			return result, fmt.Errorf("row: column %q is not present", columnName)
		}
		if err := assign(destination.Field(index), value); err != nil {
			return result, fmt.Errorf("row: decode column %q: %w", columnName, err)
		}
	}
	if fields == 0 {
		return result, fmt.Errorf("row: decode destination %T has no exported fields", result)
	}
	return result, nil
}

var decoderType = reflect.TypeFor[Decoder]()

// fieldsShadowEmbeddedDecoder reports whether rowType embeds a Decoder, declares
// mappable fields of its own, and declares no DecodeRow of its own. An interface
// assertion cannot tell a promoted DecodeRow from one the row type declares, and
// a promoted one fills only the embedded fields, so such a row type is mapped by
// its fields. A row type that declares DecodeRow itself keeps the Decoder path,
// because that method is the mapping it states and Go dispatches to it. A build
// that cannot say which of the two a DecodeRow is reports an error rather than a
// guess, because a guess fills the wrong fields silently.
func fieldsShadowEmbeddedDecoder(rowType reflect.Type) (bool, error) {
	if rowType == nil || rowType.Kind() != reflect.Struct {
		return false, nil
	}
	embedded := false
	declares := false
	for index := range rowType.NumField() {
		field := rowType.Field(index)
		if field.Anonymous {
			if implementsDecoder(field.Type) {
				embedded = true
			}
			continue
		}
		if field.PkgPath != "" {
			continue
		}
		if columnName, ok := field.Tag.Lookup("rasql"); ok && columnName == "-" {
			continue
		}
		declares = true
	}
	if !embedded || !declares {
		return false, nil
	}
	declared, err := method.Declared(rowType, "DecodeRow")
	if err != nil {
		return false, err
	}
	return !declared, nil
}

// implementsDecoder reports whether fieldType or its pointer maps result columns
// itself, so an embedded value and an embedded pointer both count.
func implementsDecoder(fieldType reflect.Type) bool {
	if fieldType.Implements(decoderType) {
		return true
	}
	return reflect.PointerTo(fieldType).Implements(decoderType)
}

func snakeCase(value string) string {
	runes := []rune(value)
	var result strings.Builder
	result.Grow(len(value))
	for index, current := range runes {
		if index > 0 && unicode.IsUpper(current) &&
			(unicode.IsLower(runes[index-1]) || unicode.IsDigit(runes[index-1]) ||
				(unicode.IsUpper(runes[index-1]) && index+1 < len(runes) && unicode.IsLower(runes[index+1]))) {
			result.WriteByte('_')
		}
		result.WriteRune(unicode.ToLower(current))
	}
	return result.String()
}

func assign(destination reflect.Value, value any) error {
	if value == nil {
		switch destination.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			destination.SetZero()
			return nil
		default:
			return fmt.Errorf("expected %s, got NULL", destination.Type())
		}
	}
	if destination.Kind() == reflect.Pointer {
		decoded := reflect.New(destination.Type().Elem())
		if err := assign(decoded.Elem(), value); err != nil {
			return err
		}
		destination.Set(decoded)
		return nil
	}
	if destination.CanAddr() {
		if scanner, ok := destination.Addr().Interface().(sql.Scanner); ok {
			if err := scanner.Scan(value); err != nil {
				return err
			}
			return nil
		}
	}
	if destination.Type() == timeType {
		decoded, err := decodeTime(value)
		if err != nil {
			return err
		}
		destination.Set(reflect.ValueOf(decoded))
		return nil
	}

	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(destination.Type()) {
		if destination.Kind() == reflect.Slice && destination.Type().Elem().Kind() == reflect.Uint8 {
			copy := reflect.MakeSlice(destination.Type(), source.Len(), source.Len())
			reflect.Copy(copy, source)
			destination.Set(copy)
			return nil
		}
		destination.Set(source)
		return nil
	}

	switch destination.Kind() {
	case reflect.Interface:
		destination.Set(source)
		return nil
	case reflect.String:
		switch source.Kind() {
		case reflect.String:
			destination.SetString(source.String())
			return nil
		case reflect.Slice:
			if source.Type().Elem().Kind() == reflect.Uint8 {
				destination.SetString(string(source.Bytes()))
				return nil
			}
		}
	case reflect.Slice:
		if destination.Type().Elem().Kind() == reflect.Uint8 {
			switch source.Kind() {
			case reflect.String:
				destination.SetBytes([]byte(source.String()))
				return nil
			case reflect.Slice:
				if source.Type().Elem().Kind() == reflect.Uint8 {
					copy := reflect.MakeSlice(destination.Type(), source.Len(), source.Len())
					reflect.Copy(copy, source)
					destination.Set(copy)
					return nil
				}
			}
		}
	case reflect.Bool:
		decoded, err := decodeBool(value)
		if err != nil {
			return err
		}
		destination.SetBool(decoded)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if isSignedInteger(source.Kind()) {
			value := source.Int()
			if destination.OverflowInt(value) {
				return fmt.Errorf("%d overflows %s", value, destination.Type())
			}
			destination.SetInt(value)
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if isUnsignedInteger(source.Kind()) {
			value := source.Uint()
			if destination.OverflowUint(value) {
				return fmt.Errorf("%d overflows %s", value, destination.Type())
			}
			destination.SetUint(value)
			return nil
		}
	case reflect.Float32, reflect.Float64:
		switch {
		case isSignedInteger(source.Kind()):
			destination.SetFloat(float64(source.Int()))
			return nil
		case isUnsignedInteger(source.Kind()):
			destination.SetFloat(float64(source.Uint()))
			return nil
		case source.Kind() == reflect.Float32 || source.Kind() == reflect.Float64:
			destination.SetFloat(source.Float())
			return nil
		}
	}
	return fmt.Errorf("expected %s, got %T", destination.Type(), value)
}

func decodeBool(value any) (bool, error) {
	decoded := reflect.ValueOf(value)
	switch decoded.Kind() {
	case reflect.Bool:
		return decoded.Bool(), nil
	case reflect.Int64:
		switch decoded.Int() {
		case 0:
			return false, nil
		case 1:
			return true, nil
		default:
			return false, fmt.Errorf("expected boolean integer 0 or 1, got %d", decoded.Int())
		}
	default:
		return false, typeError("boolean", value)
	}
}

func decodeTime(value any) (time.Time, error) {
	switch decoded := value.(type) {
	case time.Time:
		return decoded, nil
	case string:
		return parseTime(decoded)
	case []byte:
		return parseTime(string(decoded))
	default:
		return time.Time{}, typeError("time.Time", value)
	}
}

func parseTime(value string) (time.Time, error) {
	value, _, _ = strings.Cut(value, " m=")
	for _, layout := range timeLayouts {
		decoded, err := time.Parse(layout, value)
		if err == nil {
			return decoded, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time.Time value %q", value)
}

func isSignedInteger(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	default:
		return false
	}
}

func isUnsignedInteger(kind reflect.Kind) bool {
	switch kind {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}
