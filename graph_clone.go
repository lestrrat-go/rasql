package rasql

import "reflect"

// graphCloneValue copies a loaded row or graph value, so a parent that shares a
// row with another parent cannot change what that other parent received. It
// owns the top-level value and its direct slice fields, and deliberately leaves
// nested pointers, maps, interfaces, and opaque values alone because their
// mapper owns that state.
func graphCloneValue(value any) any {
	if value == nil {
		return nil
	}
	original := reflect.ValueOf(value)
	switch original.Kind() {
	case reflect.Pointer:
		if original.IsNil() {
			return value
		}
		clone := reflect.New(original.Type().Elem())
		clone.Elem().Set(original.Elem())
		if clone.Elem().Kind() == reflect.Struct {
			graphCloneDirectSlices(clone.Elem(), original.Elem())
		}
		return clone.Interface()
	case reflect.Struct:
		clone := reflect.New(original.Type()).Elem()
		clone.Set(original)
		graphCloneDirectSlices(clone, original)
		return clone.Interface()
	case reflect.Slice:
		return graphCloneSlice(original).Interface()
	default:
		return value
	}
}

func graphCloneDirectSlices(destination, source reflect.Value) {
	for index := 0; index < source.NumField(); index++ {
		from, to := source.Field(index), destination.Field(index)
		if !to.CanSet() || !from.CanInterface() {
			continue
		}
		switch from.Kind() {
		case reflect.Slice:
			to.Set(graphCloneSlice(from))
		case reflect.Pointer:
			if from.IsNil() || from.Type().Elem().Kind() != reflect.Slice {
				continue
			}
			clone := reflect.New(from.Type().Elem())
			clone.Elem().Set(graphCloneSlice(from.Elem()))
			to.Set(clone)
		}
	}
}

func graphCloneSlice(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
	reflect.Copy(clone, value)
	return clone
}
