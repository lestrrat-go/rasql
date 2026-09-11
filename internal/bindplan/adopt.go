// Package bindplan owns bound values on their way into a statement. A bound
// value is detached from whatever the caller held when it was bound, so that
// writing through the caller's copy afterwards cannot change what a statement
// sends, and so that rendering the same plan twice sends the same thing twice.
package bindplan

import (
	"database/sql"
	"errors"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/lestrrat-go/rasql/internal/planerr"
)

// ID identifies one bound value. The same value used in two places carries one
// ID, which is how a later pass recognises the two as the same bind.
type ID uint64

// ValueCopy returns a fresh copy of a bound value each time it is called.
type ValueCopy func() (any, error)

// Token carries a bound value through rendering, until Unwrap replaces it with
// the value itself.
type Token struct {
	ID         ID
	Value      any
	Codec      string
	Err        error
	Copy       ValueCopy
	PreEncoded bool
}

var nextID uint64

// NextID hands out the next bind identity.
func NextID() ID { return ID(atomic.AddUint64(&nextID, 1)) }

type snapshotIdentity struct {
	typ  reflect.Type
	kind reflect.Kind
	ptr  uintptr
	len  int
	cap  int
}

func snapshotError(err error) error {
	if err == nil {
		return nil
	}
	var planErr *planerr.Error
	if errors.As(err, &planErr) && planErr.Code == "unsnapshotable_bind" {
		return err
	}
	return planerr.Wrap("unsnapshotable_bind", "bind", err.Error(), err)
}

func Adopt[T any](value T, allowSnapshotter bool) (any, ValueCopy, error) {
	owned, copier, err := adoptBindValue(reflect.ValueOf(value), make(map[snapshotIdentity]bool), allowSnapshotter)
	if err != nil {
		return nil, nil, err
	}
	if !owned.IsValid() {
		return nil, func() (any, error) { return nil, nil }, nil
	}
	return owned.Interface(), func() (any, error) {
		copy, err := copier()
		if err != nil {
			return nil, err
		}
		return copy.Interface(), nil
	}, nil
}

func adoptBindValue(value reflect.Value, active map[snapshotIdentity]bool, allowSnapshotter bool) (reflect.Value, func() (reflect.Value, error), error) {
	if !value.IsValid() {
		return value, func() (reflect.Value, error) { return value, nil }, nil
	}
	if allowSnapshotter {
		if snapshot, ok := snapshotMethod(value); ok {
			adopted, err := snapshot()
			if err != nil {
				return reflect.Value{}, nil, err
			}
			owned := reflect.ValueOf(adopted)
			return owned, func() (reflect.Value, error) { return owned, nil }, nil
		}
	}
	if value.Type() == reflect.TypeOf(time.Time{}) || value.Kind() == reflect.Bool || value.Kind() >= reflect.Int && value.Kind() <= reflect.Float64 || value.Kind() == reflect.String {
		owned := reflect.New(value.Type()).Elem()
		owned.Set(value)
		return owned, func() (reflect.Value, error) { return owned, nil }, nil
	}
	if value.Kind() == reflect.Func || value.Kind() == reflect.Chan || value.Kind() == reflect.UnsafePointer {
		return reflect.Value{}, nil, planerr.New("unsnapshotable_bind", "bind", "mutable value is unsupported")
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		if value.IsNil() {
			zero := reflect.Zero(value.Type())
			return zero, func() (reflect.Value, error) { return zero, nil }, nil
		}
		key := snapshotKey(value)
		if active[key] {
			return reflect.Value{}, nil, planerr.New("unsnapshotable_bind", "bind", "cycle detected")
		}
		active[key] = true
		defer delete(active, key)
	}
	assign := func(dst, src reflect.Value) error {
		if !src.IsValid() {
			dst.Set(reflect.Zero(dst.Type()))
			return nil
		}
		if src.Type().AssignableTo(dst.Type()) {
			dst.Set(src)
			return nil
		}
		if src.Type().ConvertibleTo(dst.Type()) {
			dst.Set(src.Convert(dst.Type()))
			return nil
		}
		return planerr.New("unsnapshotable_bind", "bind", "incompatible adopted value")
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			zero := reflect.Zero(value.Type())
			return zero, func() (reflect.Value, error) { return zero, nil }, nil
		}
		child, childCopy, err := adoptBindValue(value.Elem(), active, allowSnapshotter)
		if err != nil {
			return reflect.Value{}, nil, err
		}
		owned := reflect.New(value.Type()).Elem()
		if err := assign(owned, child); err != nil {
			return reflect.Value{}, nil, err
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type()).Elem()
			child, err := childCopy()
			if err != nil {
				return reflect.Value{}, err
			}
			if err := assign(fresh, child); err != nil {
				return reflect.Value{}, err
			}
			return fresh, nil
		}, nil
	case reflect.Pointer:
		child, childCopy, err := adoptBindValue(value.Elem(), active, allowSnapshotter)
		if err != nil {
			return reflect.Value{}, nil, err
		}
		owned := reflect.New(value.Type().Elem())
		if err := assign(owned.Elem(), child); err != nil {
			return reflect.Value{}, nil, err
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type().Elem())
			child, err := childCopy()
			if err != nil {
				return reflect.Value{}, err
			}
			if err := assign(fresh.Elem(), child); err != nil {
				return reflect.Value{}, err
			}
			return fresh, nil
		}, nil
	case reflect.Slice:
		children := make([]func() (reflect.Value, error), value.Len())
		owned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			child, childCopy, err := adoptBindValue(value.Index(i), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assign(owned.Index(i), child); err != nil {
				return reflect.Value{}, nil, err
			}
			children[i] = childCopy
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.MakeSlice(value.Type(), len(children), len(children))
			for i, childCopy := range children {
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.Index(i), child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	case reflect.Array:
		children := make([]func() (reflect.Value, error), value.Len())
		owned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			child, childCopy, err := adoptBindValue(value.Index(i), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assign(owned.Index(i), child); err != nil {
				return reflect.Value{}, nil, err
			}
			children[i] = childCopy
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type()).Elem()
			for i, childCopy := range children {
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.Index(i), child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	case reflect.Map:
		type mapEntry struct {
			key  reflect.Value
			copy func() (reflect.Value, error)
		}
		entries := make([]mapEntry, 0, value.Len())
		owned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			if err := validateSnapshotMapKey(iter.Key()); err != nil {
				return reflect.Value{}, nil, err
			}
			child, childCopy, err := adoptBindValue(iter.Value(), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assignMapValue(owned, iter.Key(), child); err != nil {
				return reflect.Value{}, nil, err
			}
			entries = append(entries, mapEntry{key: iter.Key(), copy: childCopy})
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.MakeMapWithSize(value.Type(), len(entries))
			for _, entry := range entries {
				child, err := entry.copy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assignMapValue(fresh, entry.key, child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(sql.NamedArg{}) {
			child, childCopy, err := adoptBindValue(reflect.ValueOf(value.Interface().(sql.NamedArg).Value), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			owned := reflect.New(value.Type()).Elem()
			owned.FieldByName("Name").SetString(value.FieldByName("Name").String())
			if err := assign(owned.FieldByName("Value"), child); err != nil {
				return reflect.Value{}, nil, err
			}
			return owned, func() (reflect.Value, error) {
				fresh := reflect.New(value.Type()).Elem()
				fresh.FieldByName("Name").SetString(value.FieldByName("Name").String())
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.FieldByName("Value"), child); err != nil {
					return reflect.Value{}, err
				}
				return fresh, nil
			}, nil
		}
		children := make([]func() (reflect.Value, error), value.NumField())
		owned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath != "" {
				return reflect.Value{}, nil, planerr.New("unsnapshotable_bind", "bind", "unexported field")
			}
			child, childCopy, err := adoptBindValue(value.Field(i), active, allowSnapshotter)
			if err != nil {
				return reflect.Value{}, nil, err
			}
			if err := assign(owned.Field(i), child); err != nil {
				return reflect.Value{}, nil, err
			}
			children[i] = childCopy
		}
		return owned, func() (reflect.Value, error) {
			fresh := reflect.New(value.Type()).Elem()
			for i, childCopy := range children {
				child, err := childCopy()
				if err != nil {
					return reflect.Value{}, err
				}
				if err := assign(fresh.Field(i), child); err != nil {
					return reflect.Value{}, err
				}
			}
			return fresh, nil
		}, nil
	default:
		owned := reflect.New(value.Type()).Elem()
		owned.Set(value)
		return owned, func() (reflect.Value, error) { return owned, nil }, nil
	}
}

func snapshotKey(value reflect.Value) snapshotIdentity {
	key := snapshotIdentity{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
	if value.Kind() == reflect.Slice {
		key.len, key.cap = value.Len(), value.Cap()
	}
	return key
}

func snapshotMethod(value reflect.Value) (func() (any, error), bool) {
	method := value.MethodByName("SnapshotBind")
	if !method.IsValid() {
		return nil, false
	}
	methodType := method.Type()
	if methodType.NumIn() != 0 || methodType.NumOut() != 2 || methodType.Out(1) != reflect.TypeOf((*error)(nil)).Elem() || methodType.Out(0) != value.Type() {
		return nil, false
	}
	return func() (any, error) {
		results := method.Call(nil)
		if !results[1].IsNil() {
			return nil, snapshotError(results[1].Interface().(error))
		}
		return results[0].Interface(), nil
	}, true
}

func validateSnapshotMapKey(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return planerr.New("unsnapshotable_bind", "bind", "mutable map key")
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateSnapshotMapKey(value.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			return nil
		}
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath != "" {
				return planerr.New("unsnapshotable_bind", "bind", "mutable map key")
			}
			if err := validateSnapshotMapKey(value.Field(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func assignMapValue(dst, key, value reflect.Value) error {
	if !value.IsValid() {
		value = reflect.Zero(dst.Type().Elem())
	}
	if !value.Type().AssignableTo(dst.Type().Elem()) {
		if !value.Type().ConvertibleTo(dst.Type().Elem()) {
			return planerr.New("unsnapshotable_bind", "bind", "incompatible adopted value")
		}
		value = value.Convert(dst.Type().Elem())
	}
	dst.SetMapIndex(key, value)
	return nil
}
