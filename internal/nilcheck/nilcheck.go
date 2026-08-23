// Package nilcheck reports whether an interface value is nil, including the
// typed-nil case a plain == nil comparison misses.
package nilcheck

import "reflect"

// Is reports whether value is nil, either as an untyped nil interface or as an
// interface holding a nil pointer, map, slice, channel, function or interface.
// An entry point that takes an interface uses it to reject a nil argument with
// an error instead of dereferencing it later.
func Is(value any) bool {
	if value == nil {
		return true
	}
	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
