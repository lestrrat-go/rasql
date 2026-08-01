package row

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

// Get decodes the named value in r as T.
func Get[T any](r Row, name string) (T, error) {
	var result T
	value, ok := r.values[name]
	if !ok {
		return result, fmt.Errorf("row: column %q is not present", name)
	}
	if err := assign(reflect.ValueOf(&result).Elem(), value); err != nil {
		return result, fmt.Errorf("row: decode column %q: %w", name, err)
	}
	return result, nil
}

// Decode populates T from fields tagged with their source column name.
func Decode[T any](r Row) (T, error) {
	var result T
	destination := reflect.ValueOf(&result).Elem()
	if destination.Kind() != reflect.Struct {
		return result, fmt.Errorf("row: decode destination %T must be a struct", result)
	}

	fields := 0
	typeOfResult := destination.Type()
	for index := range destination.NumField() {
		field := typeOfResult.Field(index)
		columnName, ok := field.Tag.Lookup("rasql")
		if !ok || columnName == "-" {
			continue
		}
		columnName, _, _ = strings.Cut(columnName, ",")
		if columnName == "" {
			return result, fmt.Errorf("row: field %s has an empty rasql column name", field.Name)
		}
		if field.PkgPath != "" {
			return result, fmt.Errorf("row: field %s for column %q is not exported", field.Name, columnName)
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
		return result, fmt.Errorf("row: decode destination %T has no rasql fields", result)
	}
	return result, nil
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
		if source.Kind() == reflect.Bool {
			destination.SetBool(source.Bool())
			return nil
		}
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
