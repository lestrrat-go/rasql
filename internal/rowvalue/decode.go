package rowvalue

import (
	"database/sql"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"
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
	value, ok := r.lookup(name)
	if !ok {
		return fmt.Errorf("row: column %q is not present", name)
	}
	if err := assign(reflect.ValueOf(destination).Elem(), value); err != nil {
		return fmt.Errorf("row: decode column %q: %w", name, err)
	}
	return nil
}

// Decode populates T from rasql-tagged fields and snake-cased exported field
// names. It is the only read mapping; a row type that carries generated scan
// methods is filled by those instead, through the typed builders rather than
// through Decode.
//
// The per-type facts this needs -- which fields map to which columns -- are
// resolved once per type by planFor and cached, rather than recomputed on
// every call. A type whose plan holds an error (a bad tag or an unexported
// tagged field) reports the same error on every call, resolved before any row
// is read: an error found while planning always wins over a per-row error,
// such as a missing column, that a field reached earlier in declaration order
// would have reported first under the old, per-row walk.
func Decode[T any](r Row) (T, error) {
	var result T
	plan := planFor(reflect.TypeFor[T]())
	if plan.err != nil {
		return result, plan.err
	}
	if !plan.isStruct {
		return result, fmt.Errorf("row: decode destination %T must be a struct", result)
	}
	if len(plan.fields) == 0 {
		return result, fmt.Errorf("row: decode destination %T has no exported fields", result)
	}

	destination := reflect.ValueOf(&result).Elem()
	for _, field := range plan.fields {
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

// snakeCase derives the column name of an untagged field. It is lossy, so it
// does not round-trip every column name: a column with repeated underscores,
// such as a__b, becomes the field AB, and snakeCase("AB") is "ab" rather than
// "a__b". An untagged field whose name does not survive that round trip is
// looked up under the wrong column and reports "is not present". Give such a
// field a rasql tag naming the column exactly, which buildPlan takes in
// preference to this. Generated row types are unaffected either way, because
// they map by exact column name through ScanDestinations and never reach here.
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
	// An integer destination accepts an integer driver value of either
	// signedness, because which one a driver delivers is the driver's choice
	// rather than the column's: go-sql-driver/mysql hands back uint64 only for
	// a BIGINT UNSIGNED column and int64 for a narrower unsigned one, so a
	// generated uint64 field would otherwise fail on the narrower column. A
	// value that does not fit the destination is an error, never a wrap.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch {
		case isSignedInteger(source.Kind()):
			value := source.Int()
			if destination.OverflowInt(value) {
				return fmt.Errorf("%d overflows %s", value, destination.Type())
			}
			destination.SetInt(value)
			return nil
		case isUnsignedInteger(source.Kind()):
			value := source.Uint()
			if value > math.MaxInt64 || destination.OverflowInt(int64(value)) {
				return fmt.Errorf("%d overflows %s", value, destination.Type())
			}
			destination.SetInt(int64(value))
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch {
		case isUnsignedInteger(source.Kind()):
			value := source.Uint()
			if destination.OverflowUint(value) {
				return fmt.Errorf("%d overflows %s", value, destination.Type())
			}
			destination.SetUint(value)
			return nil
		case isSignedInteger(source.Kind()):
			value := source.Int()
			if value < 0 || destination.OverflowUint(uint64(value)) {
				return fmt.Errorf("%d overflows %s", value, destination.Type())
			}
			destination.SetUint(uint64(value))
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
	switch {
	case decoded.Kind() == reflect.Bool:
		return decoded.Bool(), nil
	case isSignedInteger(decoded.Kind()):
		return decoded.Int() != 0, nil
	case isUnsignedInteger(decoded.Kind()):
		return decoded.Uint() != 0, nil
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
