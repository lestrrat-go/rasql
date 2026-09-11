package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// These fakes serve the tests still inside this package. The same shapes live
// in executor_test.go for the tests that have moved out; this copy goes when
// the last of those tests follows them.

type runtimeFakeExecutor struct {
	calls         atomic.Int64
	rows          [][]any
	dialect       dialect.Dialect
	last          *runtimeFakeRows
	mu            sync.Mutex
	lastStatement stmt.Statement
}

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

func (e *runtimeFakeExecutor) Dialect() dialect.Dialect { return e.dialect }

func (e *runtimeFakeExecutor) Query(_ context.Context, statement stmt.Statement) (ResultRows, error) {
	e.calls.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastStatement = statement
	e.last = &runtimeFakeRows{values: e.rows}
	return e.last, nil
}

func (*runtimeFakeExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
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

func runtimeQuery(t *testing.T) Query[int64] {
	t.Helper()
	table, err := ReadTableOf[struct{}](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	column, err := BindColumn[struct{}, int64](relation, "value", "")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("value", column.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	return Select(relation.Source(), projection)
}

type runtimeDecoder struct{ schema ResultSchema }

func (d runtimeDecoder) ResultSchema() ResultSchema { return d.schema }

func (runtimeDecoder) Presence() []Presence { return nil }

func (runtimeDecoder) DecodeRow(source ScanSource, result *int64) error {
	return source.Scan(result)
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

// runtimeExecutor returns an executor and the fake behind it, so a caller that
// needs to look at what the fake saw does not have to unwrap the executor.
func runtimeExecutor(t *testing.T, rows [][]any) (Executor, *runtimeFakeExecutor) {
	t.Helper()
	raw := &runtimeFakeExecutor{rows: rows, dialect: dialect.SQLite()}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	return executor, raw
}
