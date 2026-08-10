// Package ast contains internal helpers for comparing parsed SQL trees.
package ast

import "reflect"

// Equal compares parsed SQL trees while ignoring whether identifiers were
// quoted. Dialect parsers preserve that spelling detail, but quoted lowercase
// identifiers and equivalent unquoted identifiers name the same object.
func Equal(left, right any) bool {
	return equalValue(reflect.ValueOf(left), reflect.ValueOf(right), true)
}

// EqualWithQuoted compares parsed SQL trees while preserving identifier quote
// metadata. Dialects where quoting changes identifier identity should use this
// helper instead of Equal.
func EqualWithQuoted(left, right any) bool {
	return equalValue(reflect.ValueOf(left), reflect.ValueOf(right), false)
}

func equalValue(left, right reflect.Value, ignoreQuoted bool) bool {
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
		return equalValue(left.Elem(), right.Elem(), ignoreQuoted)
	case reflect.Struct:
		for index := 0; index < left.NumField(); index++ {
			field := left.Type().Field(index)
			if ignoreQuoted && field.Name == "Quoted" && left.Field(index).Kind() == reflect.Bool {
				continue
			}
			if !equalValue(left.Field(index), right.Field(index), ignoreQuoted) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if left.Len() != right.Len() {
			return false
		}
		for index := 0; index < left.Len(); index++ {
			if !equalValue(left.Index(index), right.Index(index), ignoreQuoted) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(left.Interface(), right.Interface())
	}
}
