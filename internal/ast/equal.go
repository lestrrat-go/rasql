// Package ast contains internal helpers for comparing parsed SQL trees.
package ast

import "reflect"

// Equal compares parsed SQL trees while ignoring whether identifiers were
// quoted. Dialect parsers preserve that spelling detail, but quoted lowercase
// identifiers and equivalent unquoted identifiers name the same object.
func Equal(left, right any) bool {
	return equalValue(reflect.ValueOf(left), reflect.ValueOf(right))
}

func equalValue(left, right reflect.Value) bool {
	if !left.IsValid() || !right.IsValid() {
		return left.IsValid() == right.IsValid()
	}
	if left.Type() != right.Type() {
		return false
	}
	switch left.Kind() {
	case reflect.Interface, reflect.Pointer:
		if left.IsNil() || right.IsNil() {
			return left.IsNil() == right.IsNil()
		}
		return equalValue(left.Elem(), right.Elem())
	case reflect.Struct:
		for index := 0; index < left.NumField(); index++ {
			field := left.Type().Field(index)
			if field.Name == "Quoted" && left.Field(index).Kind() == reflect.Bool {
				continue
			}
			if !equalValue(left.Field(index), right.Field(index)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if left.Len() != right.Len() {
			return false
		}
		for index := 0; index < left.Len(); index++ {
			if !equalValue(left.Index(index), right.Index(index)) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(left.Interface(), right.Interface())
	}
}
