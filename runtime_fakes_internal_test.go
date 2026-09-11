package rasql

import (
	"database/sql"
	"errors"
	"reflect"
)

// These fakes serve the tests still inside this package. The same shapes live
// in executor_test.go for the tests that have moved out; this copy goes when
// the last of those tests follows them.

type runtimeFakeRows struct {
	values                                   [][]any
	columns                                  []string
	index                                    int
	recorded                                 int
	finished                                 int
	closed                                   int
	columnsErr, iterErr, finishErr, closeErr error
	lastFinish                               error
	lastEarly                                bool
}

func (r *runtimeFakeRows) Columns() ([]string, error) {
	if r.columnsErr != nil {
		return nil, r.columnsErr
	}
	if r.columns != nil {
		return append([]string(nil), r.columns...), nil
	}
	return []string{"value"}, nil
}

func (r *runtimeFakeRows) Next() bool { return r.index < len(r.values) }

func (r *runtimeFakeRows) Scan(destinations ...any) error {
	if r.index >= len(r.values) {
		return sql.ErrNoRows
	}
	for i, destination := range destinations {
		if i >= len(r.values[r.index]) {
			return sql.ErrNoRows
		}
		setRuntimeDestination(destination, r.values[r.index][i])
	}
	r.index++
	return nil
}

func (r *runtimeFakeRows) Close() error { r.closed++; return r.closeErr }

func (r *runtimeFakeRows) Err() error { return r.iterErr }

func (r *runtimeFakeRows) RecordRow() { r.recorded++ }

func (r *runtimeFakeRows) Finish(primary error, early bool) error {
	if r.closed == 0 {
		_ = r.Close()
	}
	r.finished++
	r.lastFinish, r.lastEarly = primary, early
	return errors.Join(primary, r.finishErr)
}

func setRuntimeDestination(destination, value any) {
	v := reflect.ValueOf(destination)
	if value == nil {
		v.Elem().SetZero()
		return
	}
	if v.Elem().Kind() == reflect.Interface {
		v.Elem().Set(reflect.ValueOf(value))
		return
	}
	if scanner, ok := destination.(sql.Scanner); ok {
		_ = scanner.Scan(value)
		return
	}
	source := reflect.ValueOf(value)
	if !source.IsValid() {
		v.Elem().SetZero()
		return
	}
	if source.Type().AssignableTo(v.Elem().Type()) {
		v.Elem().Set(source)
		return
	}
	if source.Type().ConvertibleTo(v.Elem().Type()) {
		v.Elem().Set(source.Convert(v.Elem().Type()))
	}
}
