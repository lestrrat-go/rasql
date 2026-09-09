package conformance

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type handwrittenQueryResponse struct {
	columns          []string
	rows             [][]driver.Value
	nextErr          error
	nextErrAfterRows error
	closeErr         error
	queryErr         error
}

type handwrittenExecResponse struct {
	affected    int64
	affectedErr error
	execErr     error
}

type handwrittenDriverState struct {
	mu            sync.Mutex
	queries       []handwrittenQueryResponse
	execs         []handwrittenExecResponse
	queryCalls    int
	execCalls     int
	queryArgs     [][]driver.NamedValue
	execArgs      [][]driver.NamedValue
	querySQL      []string
	execSQL       []string
	rowsCloses    int
	affectedCalls int
}

type handwrittenConnector struct{ state *handwrittenDriverState }

func (c handwrittenConnector) Connect(context.Context) (driver.Conn, error) {
	return handwrittenConn(c), nil
}

func (c handwrittenConnector) Driver() driver.Driver { return handwrittenDriver{} }

type handwrittenDriver struct{}

func (handwrittenDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("handwritten driver requires sql.OpenDB")
}

type handwrittenConn struct{ state *handwrittenDriverState }

func (c handwrittenConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c handwrittenConn) Close() error              { return nil }
func (c handwrittenConn) Begin() (driver.Tx, error) { return handwrittenTx{}, nil }

func (c handwrittenConn) CheckNamedValue(value *driver.NamedValue) error {
	converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
	if err != nil {
		return err
	}
	value.Value = converted
	return nil
}

func (c handwrittenConn) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	c.state.queryCalls++
	c.state.querySQL = append(c.state.querySQL, statement)
	c.state.queryArgs = append(c.state.queryArgs, cloneHandwrittenNamedValues(args))
	if len(c.state.queries) == 0 {
		c.state.mu.Unlock()
		return nil, errors.New("handwritten query script exhausted")
	}
	response := c.state.queries[0]
	c.state.queries = c.state.queries[1:]
	c.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if response.queryErr != nil {
		return nil, response.queryErr
	}
	return &handwrittenDriverRows{
		state: c.state, columns: response.columns, rows: response.rows,
		nextErr: response.nextErr, nextErrAfterRows: response.nextErrAfterRows,
		closeErr: response.closeErr,
	}, nil
}

func (c handwrittenConn) ExecContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	c.state.execCalls++
	c.state.execSQL = append(c.state.execSQL, statement)
	c.state.execArgs = append(c.state.execArgs, cloneHandwrittenNamedValues(args))
	response := c.state.execs[0]
	c.state.execs = c.state.execs[1:]
	c.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if response.execErr != nil {
		return nil, response.execErr
	}
	return handwrittenResult{state: c.state, affected: response.affected, affectedErr: response.affectedErr}, nil
}

func cloneHandwrittenNamedValues(values []driver.NamedValue) []driver.NamedValue {
	result := make([]driver.NamedValue, len(values))
	for index, value := range values {
		result[index] = value
		cloned, _ := cloneHandwrittenValue(value.Value)
		result[index].Value = cloned
	}
	return result
}

type handwrittenTx struct{}

func (handwrittenTx) Commit() error   { return nil }
func (handwrittenTx) Rollback() error { return nil }

type handwrittenResult struct {
	state       *handwrittenDriverState
	affected    int64
	affectedErr error
}

type handwrittenNilResultExecer struct {
	calls         int
	affectedCalls int
}

func (e *handwrittenNilResultExecer) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	e.calls++
	return nil, nil
}

var (
	_ handwrittenExecer  = (*sql.DB)(nil)
	_ handwrittenExecer  = (*sql.Tx)(nil)
	_ handwrittenQuerier = (*sql.DB)(nil)
	_ handwrittenQuerier = (*sql.Tx)(nil)
)

func (r handwrittenResult) LastInsertId() (int64, error) { return 0, nil }
func (r handwrittenResult) RowsAffected() (int64, error) {
	r.state.mu.Lock()
	r.state.affectedCalls++
	r.state.mu.Unlock()
	return r.affected, r.affectedErr
}

type handwrittenDriverRows struct {
	state            *handwrittenDriverState
	columns          []string
	rows             [][]driver.Value
	nextErr          error
	nextErrAfterRows error
	closeErr         error
	index            int
}

func (r *handwrittenDriverRows) Columns() []string { return r.columns }

func (r *handwrittenDriverRows) Next(destination []driver.Value) error {
	if r.nextErr != nil {
		err := r.nextErr
		r.nextErr = nil
		return err
	}
	if r.index >= len(r.rows) {
		if r.nextErrAfterRows != nil {
			err := r.nextErrAfterRows
			r.nextErrAfterRows = nil
			return err
		}
		return io.EOF
	}
	copy(destination, r.rows[r.index])
	r.index++
	return nil
}

func (r *handwrittenDriverRows) Close() error {
	r.state.mu.Lock()
	r.state.rowsCloses++
	r.state.mu.Unlock()
	return r.closeErr
}

func openHandwrittenDB(state *handwrittenDriverState) *sql.DB {
	database := sql.OpenDB(handwrittenConnector{state: state})
	database.SetMaxOpenConns(1)
	return database
}

func TestHandwrittenObserverExecContext(t *testing.T) {
	callErr := errors.New("exec call")
	affectedErr := errors.New("affected rows")
	cases := []struct {
		name      string
		response  handwrittenExecResponse
		wantErr   error
		wantValid bool
	}{
		{name: "success", response: handwrittenExecResponse{affected: 3}, wantValid: true},
		{name: "call error", response: handwrittenExecResponse{execErr: callErr}, wantErr: callErr},
		{name: "affected error", response: handwrittenExecResponse{affectedErr: affectedErr}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &handwrittenDriverState{execs: []handwrittenExecResponse{tc.response}}
			database := openHandwrittenDB(state)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			observer := newHandwrittenObserver()
			metadata, err := measuredStatement(roleMutation, "write", 0)
			require.NoError(t, err)
			bytes := []byte("before")
			result, err := observer.ExecContext(t.Context(), database, metadata, "UPDATE values SET body = ?", bytes, sql.Named("named", []byte("named")))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
			bytes[0] = 'X'
			records, snapshotErr := observer.Snapshot()
			require.NoError(t, snapshotErr)
			require.Len(t, records, 1)
			require.Equal(t, 1, state.execCalls)
			require.Equal(t, "mutation", string(records[0].Role))
			require.Equal(t, "write", records[0].LogicalParent)
			require.Equal(t, 0, records[0].StatementIndex)
			require.Equal(t, "UPDATE values SET body = ?", records[0].SQL)
			require.Equal(t, "exec", records[0].Kind)
			require.Equal(t, "execution", records[0].Phase)
			require.True(t, records[0].Started)
			require.True(t, records[0].Completed)
			require.Equal(t, 1, records[0].CompletionCount)
			require.Equal(t, tc.wantValid, records[0].RowsAffectedValid)
			require.Equal(t, []string{"UPDATE values SET body = ?"}, state.execSQL)
			require.Len(t, state.execArgs, 1)
			require.Equal(t, 1, state.execArgs[0][0].Ordinal)
			require.Equal(t, []byte("before"), state.execArgs[0][0].Value)
			require.Equal(t, "named", state.execArgs[0][1].Name)
			require.Equal(t, 2, state.execArgs[0][1].Ordinal)
			require.Equal(t, []byte("named"), state.execArgs[0][1].Value)
			if tc.name != "call error" {
				require.Equal(t, 1, state.affectedCalls)
			}
			if tc.wantValid {
				require.Equal(t, int64(3), records[0].RowsAffected)
			}
			if tc.name == "affected error" {
				require.ErrorIs(t, records[0].RowsAffectedErr, affectedErr)
			}
			if tc.wantErr != nil {
				require.ErrorIs(t, records[0].Err, tc.wantErr)
			} else {
				require.NoError(t, records[0].Err)
			}
			require.Equal(t, []byte("before"), records[0].Args[0])
		})
	}
}

func TestHandwrittenObserverQueryLifecycle(t *testing.T) {
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{columns: []string{"id", "name"}, rows: [][]driver.Value{{int64(1), "one"}, {int64(2), "two"}}}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "read", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT id, name FROM values")
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.True(t, rows.Next())
	var id int64
	var name string
	require.NoError(t, rows.Scan(&id, &name))
	require.Equal(t, int64(2), id)
	require.Equal(t, "two", name)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(1), records[0].RowsConsumed)
	require.Equal(t, [][]any{{int64(2), "two"}}, records[0].RowValues)
	require.False(t, records[0].EarlyClose)
	require.Equal(t, 1, records[0].CompletionCount)
	require.Equal(t, []string{"SELECT id, name FROM values"}, state.querySQL)
	require.Equal(t, 1, state.rowsCloses)
}

func TestHandwrittenObserverQueryFailure(t *testing.T) {
	queryErr := errors.New("query failed")
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{queryErr: queryErr}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "failure", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT failure", nil, sql.Named("failure", int64(3)))
	require.ErrorIs(t, err, queryErr)
	require.Nil(t, rows)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, statementObservation{
		Role:            roleRead,
		LogicalParent:   "failure",
		StatementIndex:  0,
		SQL:             "SELECT failure",
		Args:            []any{nil, sql.Named("failure", int64(3))},
		Kind:            "query",
		Phase:           "execution",
		Started:         true,
		Completed:       true,
		CompletionCount: 1,
		Err:             queryErr,
	}, records[0])
	require.Zero(t, records[0].RowsConsumed)
	require.ErrorIs(t, records[0].Err, queryErr)
	require.Equal(t, 1, state.queryCalls)
	require.Equal(t, []string{"SELECT failure"}, state.querySQL)
	require.Len(t, state.queryArgs, 1)
	require.Equal(t, 2, state.queryArgs[0][1].Ordinal)
	require.Equal(t, "failure", state.queryArgs[0][1].Name)
	require.Equal(t, 0, state.rowsCloses)
}

func TestHandwrittenObserverCloseAndErrors(t *testing.T) {
	closeErr := errors.New("close failed")
	nextErr := errors.New("next failed")
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{
		{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}, closeErr: closeErr},
		{columns: []string{"id"}, rows: [][]driver.Value{{"bad"}}},
		{columns: []string{"id"}, nextErr: nextErr, closeErr: closeErr},
	}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "read", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.True(t, rows.Next())
	var id int64
	require.NoError(t, rows.Scan(&id))
	require.ErrorIs(t, rows.Close(), closeErr)
	require.ErrorIs(t, rows.Close(), closeErr)
	require.Equal(t, 1, state.rowsCloses)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.True(t, records[0].EarlyClose)
	require.Equal(t, int64(1), records[0].RowsConsumed)
	require.Equal(t, "consumption", records[0].Phase)
	require.Equal(t, 1, records[0].CompletionCount)
	require.ErrorIs(t, records[0].CloseErr, closeErr)
	require.ErrorIs(t, records[0].Err, closeErr)

	metadata, err = measuredStatement(roleRead, "read", 1)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.True(t, rows.Next())
	scanErr := rows.Scan(&id)
	require.Error(t, scanErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.Error(t, records[1].ScanErr)
	require.EqualError(t, records[1].ScanErr, scanErr.Error())
	require.EqualError(t, records[1].Err, scanErr.Error())
	require.Equal(t, 1, records[1].CompletionCount)

	metadata, err = measuredStatement(roleRead, "read", 2)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.ErrorIs(t, rows.Err(), nextErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.ErrorIs(t, records[2].RowsErr, nextErr)
	require.ErrorIs(t, records[2].Err, nextErr)
	require.Equal(t, 1, records[2].CompletionCount)
	require.Equal(t, "consumption", records[2].Phase)
}

func TestHandwrittenObserverExhaustionAndCombinedErrors(t *testing.T) {
	nextErr := errors.New("exhaustion failed")
	closeErr := errors.New("exhaustion close failed")
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{
		{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}, nextErrAfterRows: nextErr},
		{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}, nextErrAfterRows: nextErr, closeErr: closeErr},
		{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}, {int64(2)}}, closeErr: closeErr},
		{columns: []string{"id"}, rows: [][]driver.Value{{"bad"}}, closeErr: closeErr},
		{columns: []string{"id"}, nextErr: nextErr, closeErr: closeErr},
		{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}, closeErr: closeErr},
	}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()

	metadata, err := measuredStatement(roleRead, "exhaustion", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT id", sql.Named("id", int64(1)))
	require.NoError(t, err)
	var id int64
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id))
	require.False(t, rows.Next())
	require.ErrorIs(t, rows.Err(), nextErr)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.ErrorIs(t, records[0].RowsErr, nextErr)
	require.Nil(t, records[0].CloseErr)
	require.ErrorIs(t, records[0].Err, nextErr)
	require.Equal(t, int64(1), records[0].RowsConsumed)
	require.False(t, records[0].EarlyClose)
	require.Equal(t, 1, state.rowsCloses)

	metadata, err = measuredStatement(roleRead, "exhaustion", 1)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id))
	require.False(t, rows.Next())
	require.ErrorIs(t, rows.Err(), nextErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.ErrorIs(t, records[1].RowsErr, nextErr)
	// database/sql auto-closes after driver Next fails, so the raw close error is unavailable.
	require.Nil(t, records[1].CloseErr)
	require.NotErrorIs(t, records[1].Err, closeErr)
	require.Equal(t, 2, state.rowsCloses)

	metadata, err = measuredStatement(roleRead, "exhaustion", 2)
	require.NoError(t, err)
	err = observer.QueryOne(t.Context(), database, metadata, "SELECT id", []any{&id}, sql.Named("cardinality", int64(2)))
	require.ErrorIs(t, err, errHandwrittenCardinality)
	require.ErrorIs(t, err, closeErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.ErrorIs(t, records[2].CloseErr, closeErr)
	require.ErrorIs(t, records[2].Err, errHandwrittenCardinality)
	require.ErrorIs(t, records[2].Err, closeErr)
	require.Equal(t, roleRead, records[2].Role)
	require.Equal(t, "exhaustion", records[2].LogicalParent)
	require.Equal(t, 2, records[2].StatementIndex)
	require.Equal(t, "SELECT id", records[2].SQL)
	require.Equal(t, []any{sql.Named("cardinality", int64(2))}, records[2].Args)
	require.Equal(t, "query", records[2].Kind)
	require.Equal(t, "consumption", records[2].Phase)
	require.True(t, records[2].Started)
	require.True(t, records[2].Completed)
	require.Equal(t, 1, records[2].CompletionCount)
	require.Equal(t, int64(1), records[2].RowsConsumed)
	require.Equal(t, [][]any{{int64(1)}}, records[2].RowValues)
	require.True(t, records[2].EarlyClose)
	require.Equal(t, 3, state.rowsCloses)
	require.Len(t, state.queryArgs, 3)
	require.Equal(t, "cardinality", state.queryArgs[2][0].Name)
	require.Equal(t, 1, state.queryArgs[2][0].Ordinal)
	require.Equal(t, int64(2), state.queryArgs[2][0].Value)

	metadata, err = measuredStatement(roleRead, "exhaustion", 3)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.True(t, rows.Next())
	scanErr := rows.Scan(&id)
	require.Error(t, scanErr)
	var conversionWrapper interface {
		Error() string
		Unwrap() error
	}
	require.ErrorAs(t, scanErr, &conversionWrapper)
	require.True(t, strings.Contains(conversionWrapper.Error(), "converting driver.Value"))
	require.ErrorIs(t, scanErr, closeErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.Error(t, records[3].ScanErr)
	require.ErrorIs(t, scanErr, records[3].ScanErr)
	require.ErrorIs(t, records[3].CloseErr, closeErr)
	require.ErrorIs(t, records[3].Err, records[3].ScanErr)
	require.ErrorIs(t, records[3].Err, closeErr)
	require.Equal(t, roleRead, records[3].Role)
	require.Equal(t, "exhaustion", records[3].LogicalParent)
	require.Equal(t, 3, records[3].StatementIndex)
	require.Equal(t, "SELECT id", records[3].SQL)
	require.Empty(t, records[3].Args)
	require.Equal(t, "query", records[3].Kind)
	require.Equal(t, "consumption", records[3].Phase)
	require.True(t, records[3].Started)
	require.True(t, records[3].Completed)
	require.Equal(t, 1, records[3].CompletionCount)
	require.Equal(t, int64(0), records[3].RowsConsumed)
	require.Empty(t, records[3].RowValues)
	require.True(t, records[3].EarlyClose)
	require.Equal(t, 4, state.rowsCloses)

	metadata, err = measuredStatement(roleRead, "exhaustion", 4)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.ErrorIs(t, rows.Err(), nextErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.ErrorIs(t, records[4].RowsErr, nextErr)
	require.Nil(t, records[4].CloseErr)
	require.NotErrorIs(t, records[4].Err, closeErr)
	require.Equal(t, 5, state.rowsCloses)

	metadata, err = measuredStatement(roleRead, "exhaustion", 5)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, metadata, "SELECT id")
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id))
	require.False(t, rows.Next())
	require.ErrorIs(t, rows.Err(), closeErr)
	records, err = observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, roleRead, records[5].Role)
	require.Equal(t, "exhaustion", records[5].LogicalParent)
	require.Equal(t, 5, records[5].StatementIndex)
	require.Equal(t, "SELECT id", records[5].SQL)
	require.Empty(t, records[5].Args)
	require.Equal(t, "query", records[5].Kind)
	require.Equal(t, "consumption", records[5].Phase)
	require.True(t, records[5].Started)
	require.True(t, records[5].Completed)
	require.Equal(t, 1, records[5].CompletionCount)
	require.Equal(t, int64(1), records[5].RowsConsumed)
	require.Equal(t, [][]any{{int64(1)}}, records[5].RowValues)
	require.False(t, records[5].EarlyClose)
	require.ErrorIs(t, records[5].RowsErr, closeErr)
	require.ErrorIs(t, records[5].Err, closeErr)
	require.Nil(t, records[5].CloseErr)
	require.Equal(t, 6, state.rowsCloses)
}

func TestHandwrittenObserverQueryOne(t *testing.T) {
	cases := []struct {
		name      string
		rows      [][]driver.Value
		wantErr   error
		wantEarly bool
		wantCount int64
	}{
		{name: "zero", wantErr: sql.ErrNoRows},
		{name: "one", rows: [][]driver.Value{{int64(1), "one"}}, wantCount: 1},
		{name: "many", rows: [][]driver.Value{{int64(1), "one"}, {int64(2), "two"}}, wantErr: errHandwrittenCardinality, wantEarly: true, wantCount: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{columns: []string{"id", "name"}, rows: tc.rows}}}
			database := openHandwrittenDB(state)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			observer := newHandwrittenObserver()
			metadata, err := measuredStatement(roleRead, "one", 0)
			require.NoError(t, err)
			var id int64
			var name string
			err = observer.QueryOne(t.Context(), database, metadata, "SELECT id, name", []any{&id, &name}, sql.Named("query-one", int64(9)))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			records, snapshotErr := observer.Snapshot()
			require.NoError(t, snapshotErr)
			require.Equal(t, roleRead, records[0].Role)
			require.Equal(t, "one", records[0].LogicalParent)
			require.Equal(t, 0, records[0].StatementIndex)
			require.Equal(t, "SELECT id, name", records[0].SQL)
			require.Equal(t, []any{sql.Named("query-one", int64(9))}, records[0].Args)
			require.Equal(t, "query", records[0].Kind)
			require.Equal(t, "consumption", records[0].Phase)
			require.True(t, records[0].Started)
			require.True(t, records[0].Completed)
			require.Equal(t, 1, records[0].CompletionCount)
			require.Equal(t, tc.wantCount, records[0].RowsConsumed)
			require.Equal(t, tc.wantEarly, records[0].EarlyClose)
			require.Equal(t, []string{"SELECT id, name"}, state.querySQL)
			require.Equal(t, 1, state.queryCalls)
			require.Len(t, state.queryArgs, 1)
			require.Equal(t, "query-one", state.queryArgs[0][0].Name)
			require.Equal(t, 1, state.queryArgs[0][0].Ordinal)
			require.Equal(t, int64(9), state.queryArgs[0][0].Value)
			require.Equal(t, 1, state.rowsCloses)
			if tc.name == "zero" {
				require.ErrorIs(t, records[0].Err, sql.ErrNoRows)
				require.Empty(t, records[0].RowValues)
			}
			if tc.name == "one" {
				require.Equal(t, [][]any{{int64(1), "one"}}, records[0].RowValues)
				require.NoError(t, records[0].Err)
			}
			if tc.name == "many" {
				require.ErrorIs(t, records[0].Err, errHandwrittenCardinality)
				require.Equal(t, [][]any{{int64(1), "one"}}, records[0].RowValues)
			}
		})
	}
}

func TestHandwrittenObserverMetadataAndSnapshots(t *testing.T) {
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "parent", 0)
	require.NoError(t, err)
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{
		{columns: []string{"value"}, rows: [][]driver.Value{{[]byte("bytes")}}},
		{columns: []string{"value"}, rows: [][]driver.Value{{[]byte("bytes")}}},
		{columns: []string{"value"}, rows: [][]driver.Value{{[]byte("bytes")}}},
	}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	direct := []byte("argument")
	named := []byte("named")
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT value", direct, sql.Named("payload", named))
	require.NoError(t, err)
	require.Len(t, state.queryArgs, 1)
	require.Equal(t, "", state.queryArgs[0][0].Name)
	require.Equal(t, 1, state.queryArgs[0][0].Ordinal)
	require.Equal(t, []byte("argument"), state.queryArgs[0][0].Value)
	require.Equal(t, "payload", state.queryArgs[0][1].Name)
	require.Equal(t, 2, state.queryArgs[0][1].Ordinal)
	require.Equal(t, []byte("named"), state.queryArgs[0][1].Value)
	direct[0] = 'D'
	named[0] = 'N'
	require.Equal(t, []byte("argument"), state.queryArgs[0][0].Value)
	require.Equal(t, []byte("named"), state.queryArgs[0][1].Value)
	require.True(t, rows.Next())
	value := []byte{}
	require.NoError(t, rows.Scan(&value))
	value[0] = 'X'
	require.False(t, rows.Next())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	value[1] = 'X'
	require.Equal(t, []byte("bytes"), records[0].RowValues[0][0])
	require.Equal(t, []byte("argument"), records[0].Args[0])
	require.Equal(t, sql.Named("payload", []byte("named")), records[0].Args[1])
	state.queryArgs[0][0].Value.([]byte)[0] = 'S'
	state.queryArgs[0][1].Value.([]byte)[0] = 'T'
	records[0].Args[0].([]byte)[0] = 'R'
	records[0].Args[1].(sql.NamedArg).Value.([]byte)[0] = 'M'
	records[0].RowValues[0][0].([]byte)[0] = 'Q'
	secondRecords, err := observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, []byte("argument"), secondRecords[0].Args[0])
	require.Equal(t, sql.Named("payload", []byte("named")), secondRecords[0].Args[1])
	require.Equal(t, []byte("bytes"), secondRecords[0].RowValues[0][0])
	require.Equal(t, []byte("Rrgument"), records[0].Args[0])
	require.Equal(t, sql.Named("payload", []byte("Mamed")), records[0].Args[1])
	require.Equal(t, []byte("Qytes"), records[0].RowValues[0][0])
	require.Equal(t, []byte("Srgument"), state.queryArgs[0][0].Value)
	require.Equal(t, []byte("Tamed"), state.queryArgs[0][1].Value)

	openObserver := newHandwrittenObserver()
	openRows, err := openObserver.QueryContext(t.Context(), database, metadata, "SELECT value")
	require.NoError(t, err)
	require.True(t, openRows.Next())
	_, err = openObserver.Snapshot()
	require.Error(t, err)
	require.NoError(t, openRows.Close())

	_, err = observer.QueryContext(t.Context(), database, metadata, "duplicate")
	require.Error(t, err)
	nextMetadata, err := measuredStatement(roleRead, "parent", 2)
	require.NoError(t, err)
	_, err = observer.QueryContext(t.Context(), database, nextMetadata, "skipped")
	require.Error(t, err)
	otherMetadata, err := measuredStatement(roleRead, "other", 0)
	require.NoError(t, err)
	rows, err = observer.QueryContext(t.Context(), database, otherMetadata, "SELECT value")
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	require.Equal(t, 3, state.queryCalls)
}

func TestHandwrittenObserverNullableSnapshots(t *testing.T) {
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{
		columns: []string{"flag", "number", "text", "when"},
		rows:    [][]driver.Value{{nil, nil, nil, nil}},
	}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "nullable", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT nullable")
	require.NoError(t, err)
	require.True(t, rows.Next())
	var flag sql.NullBool
	var number sql.NullInt64
	var text sql.NullString
	var when sql.NullTime
	require.NoError(t, rows.Scan(&flag, &number, &text, &when))
	require.False(t, flag.Valid)
	require.False(t, number.Valid)
	require.False(t, text.Valid)
	require.False(t, when.Valid)
	require.False(t, rows.Next())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, [][]any{{nil, nil, nil, nil}}, records[0].RowValues)
}

func TestHandwrittenObserverCancellationAndVerification(t *testing.T) {
	querier := &handwrittenCanceledQuerier{}
	observer := newHandwrittenObserver()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	metadata, err := verificationStatement("verify", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(ctx, querier, metadata, "SELECT 1", sql.Named("cancel", int64(8)))
	require.Nil(t, rows)
	require.ErrorIs(t, err, context.Canceled)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.True(t, records[0].Verification)
	require.Equal(t, statementObservation{
		Role:            roleVerification,
		LogicalParent:   "verify",
		StatementIndex:  0,
		Verification:    true,
		SQL:             "SELECT 1",
		Args:            []any{sql.Named("cancel", int64(8))},
		Kind:            "query",
		Phase:           "execution",
		Started:         true,
		Completed:       true,
		CompletionCount: 1,
		Err:             context.Canceled,
	}, records[0])
	require.ErrorIs(t, records[0].Err, context.Canceled)
	require.Equal(t, 1, querier.calls)
	require.Equal(t, "SELECT 1", querier.statement)
	require.Equal(t, []any{sql.Named("cancel", int64(8))}, querier.args)
	_, err = measuredStatement(roleVerification, "bad", 0)
	require.Error(t, err)
	_, err = verificationStatement("", 0)
	require.Error(t, err)
}

func TestHandwrittenObserverCancellationDropsErroneousRowsHandle(t *testing.T) {
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{columns: []string{"id"}}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	rowsHandle, err := database.QueryContext(t.Context(), "SELECT id")
	require.NoError(t, err)
	querier := &handwrittenCanceledQuerier{rows: rowsHandle}
	observer := newHandwrittenObserver()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	metadata, err := verificationStatement("erroneous-rows", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(ctx, querier, metadata, "SELECT id")
	require.Nil(t, rows)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, rowsHandle.Close())
}

func TestHandwrittenObserverDBAndTxInterfaces(t *testing.T) {
	state := &handwrittenDriverState{
		execs:   []handwrittenExecResponse{{affected: 1}},
		queries: []handwrittenQueryResponse{{columns: []string{"id"}, rows: [][]driver.Value{{int64(4)}}}},
	}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	tx, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	observer := newHandwrittenObserver()
	execMetadata, err := measuredStatement(roleMutation, "tx", 0)
	require.NoError(t, err)
	_, err = observer.ExecContext(t.Context(), tx, execMetadata, "UPDATE values SET id = ?", int64(4))
	require.NoError(t, err)
	queryMetadata, err := measuredStatement(roleRead, "tx", 1)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), tx, queryMetadata, "SELECT id WHERE id = ?", int64(4))
	require.NoError(t, err)
	require.True(t, rows.Next())
	var id int64
	require.NoError(t, rows.Scan(&id))
	require.False(t, rows.Next())
	require.NoError(t, tx.Commit())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "execution", records[0].Phase)
	require.Equal(t, "consumption", records[1].Phase)
	require.Equal(t, []string{"UPDATE values SET id = ?"}, state.execSQL)
	require.Equal(t, []string{"SELECT id WHERE id = ?"}, state.querySQL)
}

func TestHandwrittenObserverExecNilResult(t *testing.T) {
	execer := &handwrittenNilResultExecer{}
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleMutation, "nil-result", 0)
	require.NoError(t, err)
	result, err := observer.ExecContext(t.Context(), execer, metadata, "UPDATE values SET name = ?", "changed")
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, execer.calls)
	require.Equal(t, 0, execer.affectedCalls)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, statementObservation{
		Role:              roleMutation,
		LogicalParent:     "nil-result",
		StatementIndex:    0,
		SQL:               "UPDATE values SET name = ?",
		Args:              []any{"changed"},
		Kind:              "exec",
		Phase:             "execution",
		Started:           true,
		Completed:         true,
		CompletionCount:   1,
		RowsAffectedErr:   errHandwrittenMissingResult,
		RowsAffectedValid: false,
	}, records[0])
	require.False(t, records[0].RowsAffectedValid)
	require.ErrorIs(t, records[0].RowsAffectedErr, errHandwrittenMissingResult)
	require.NoError(t, records[0].Err)
}

func TestHandwrittenObserverRecordsFullQuery(t *testing.T) {
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{
		columns: []string{"id", "name"},
		rows:    [][]driver.Value{{int64(1), "one"}, {int64(2), "two"}},
	}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "full-query", 0)
	require.NoError(t, err)
	argument := []byte("argument")
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT id, name FROM values WHERE id = ?", sql.Named("id", argument))
	require.NoError(t, err)
	var id int64
	var name string
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id, &name))
	require.Equal(t, int64(1), id)
	require.Equal(t, "one", name)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id, &name))
	require.Equal(t, int64(2), id)
	require.Equal(t, "two", name)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Len(t, records, 1)
	record := records[0]
	require.Equal(t, statementObservation{
		Role:            roleRead,
		LogicalParent:   "full-query",
		StatementIndex:  0,
		SQL:             "SELECT id, name FROM values WHERE id = ?",
		Args:            []any{sql.Named("id", []byte("argument"))},
		Kind:            "query",
		Phase:           "consumption",
		Started:         true,
		Completed:       true,
		CompletionCount: 1,
		RowsConsumed:    2,
		RowValues:       [][]any{{int64(1), "one"}, {int64(2), "two"}},
	}, record)
	require.Equal(t, []string{"SELECT id, name FROM values WHERE id = ?"}, state.querySQL)
	require.Len(t, state.queryArgs, 1)
	require.Equal(t, "id", state.queryArgs[0][0].Name)
	require.Equal(t, 1, state.queryArgs[0][0].Ordinal)
	require.Equal(t, []byte("argument"), state.queryArgs[0][0].Value)
}

func TestHandwrittenObserverQueryOneNoRowsTerminal(t *testing.T) {
	noRowsErr := errors.New("driver had no rows")
	closeOnlyErr := errors.New("driver close only")
	cases := []struct {
		name     string
		response handwrittenQueryResponse
		wantErr  error
	}{
		{name: "sql no rows", wantErr: sql.ErrNoRows},
		{name: "driver rows error", response: handwrittenQueryResponse{nextErr: noRowsErr}, wantErr: noRowsErr},
		{name: "close error", response: handwrittenQueryResponse{closeErr: closeOnlyErr}, wantErr: closeOnlyErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &handwrittenDriverState{queries: []handwrittenQueryResponse{tc.response}}
			database := openHandwrittenDB(state)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			observer := newHandwrittenObserver()
			metadata, err := measuredStatement(roleRead, "query-one-"+tc.name, 0)
			require.NoError(t, err)
			var id int64
			err = observer.QueryOne(t.Context(), database, metadata, "SELECT id", []any{&id}, sql.Named("arg", int64(7)))
			require.ErrorIs(t, err, tc.wantErr)
			records, snapshotErr := observer.Snapshot()
			require.NoError(t, snapshotErr)
			require.Len(t, records, 1)
			require.Equal(t, roleRead, records[0].Role)
			require.Equal(t, "query-one-"+tc.name, records[0].LogicalParent)
			require.Equal(t, 0, records[0].StatementIndex)
			require.Equal(t, "SELECT id", records[0].SQL)
			require.Equal(t, []any{sql.Named("arg", int64(7))}, records[0].Args)
			require.Equal(t, "query", records[0].Kind)
			require.Equal(t, "consumption", records[0].Phase)
			require.True(t, records[0].Started)
			require.True(t, records[0].Completed)
			require.Equal(t, 1, records[0].CompletionCount)
			require.Zero(t, records[0].RowsConsumed)
			require.Empty(t, records[0].RowValues)
			require.False(t, records[0].EarlyClose)
			require.ErrorIs(t, records[0].Err, tc.wantErr)
			require.Equal(t, []string{"SELECT id"}, state.querySQL)
			require.Len(t, state.queryArgs, 1)
			require.Equal(t, 1, state.rowsCloses)
			if tc.name == "driver rows error" {
				require.ErrorIs(t, records[0].RowsErr, tc.wantErr)
			}
			if tc.name == "close error" {
				// database/sql reports an EOF close failure through Rows.Err, not the later Rows.Close call.
				require.ErrorIs(t, records[0].RowsErr, closeOnlyErr)
				require.Nil(t, records[0].CloseErr)
			}
		})
	}
}

func TestHandwrittenObserverMetadataAndDestinationErrors(t *testing.T) {
	for _, parent := range []string{"", " ", "\t\n"} {
		_, err := measuredStatement(roleRead, parent, 0)
		require.Error(t, err)
		_, err = verificationStatement(parent, 0)
		require.Error(t, err)
	}
	_, err := measuredStatement(roleRead, "parent", -1)
	require.Error(t, err)
	_, err = verificationStatement("parent", -1)
	require.Error(t, err)

	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{
		columns: []string{"value"}, rows: [][]driver.Value{{int64(1)}},
	}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "destination", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT value")
	require.NoError(t, err)
	require.True(t, rows.Next())
	var unsupported any
	scanErr := rows.Scan(&unsupported)
	require.ErrorIs(t, scanErr, errHandwrittenUnsupportedDestination)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.ErrorIs(t, records[0].ScanErr, errHandwrittenUnsupportedDestination)
	require.ErrorIs(t, records[0].Err, errHandwrittenUnsupportedDestination)
	require.Equal(t, 1, state.rowsCloses)

	invalid := statementMetadata{Role: roleRead, LogicalParent: "bad", StatementIndex: 0, Verification: true}
	_, err = observer.QueryContext(t.Context(), database, invalid, "invalid")
	require.Error(t, err)
	invalid = statementMetadata{Role: roleVerification, LogicalParent: "bad", StatementIndex: 0}
	_, err = observer.QueryContext(t.Context(), database, invalid, "invalid")
	require.Error(t, err)
}

func TestHandwrittenObserverMetadataDoesNotAdvance(t *testing.T) {
	roles := []statementRole{
		roleRead, roleReport, roleRoot, roleTasks, roleAssignees, roleMutation,
		roleSavepointBegin, roleSavepointRollback, roleSavepointRelease, roleSentinel,
	}
	responses := make([]handwrittenQueryResponse, 0, len(roles)*2)
	for range roles {
		responses = append(responses,
			handwrittenQueryResponse{columns: []string{"id"}},
			handwrittenQueryResponse{columns: []string{"id"}},
		)
	}
	state := &handwrittenDriverState{queries: responses}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	for index, role := range roles {
		parent := fmt.Sprintf("role-%d", index)
		_, err := measuredStatement(role, parent, 0)
		require.NoError(t, err)
		_, err = measuredStatement(roleVerification, parent, 0)
		require.Error(t, err)
		_, err = verificationStatement(parent, 0)
		require.NoError(t, err)
		badVerification := statementMetadata{Role: roleRead, LogicalParent: parent, StatementIndex: 0, Verification: true}
		_, err = observer.QueryContext(t.Context(), database, badVerification, "invalid")
		require.Error(t, err)
		badRole := statementMetadata{Role: roleVerification, LogicalParent: parent, StatementIndex: 0}
		_, err = observer.QueryContext(t.Context(), database, badRole, "invalid")
		require.Error(t, err)
		blank := statementMetadata{Role: role, LogicalParent: " \t", StatementIndex: 0}
		_, err = observer.QueryContext(t.Context(), database, blank, "invalid")
		require.Error(t, err)
		negative := statementMetadata{Role: role, LogicalParent: parent, StatementIndex: -1}
		_, err = observer.QueryContext(t.Context(), database, negative, "invalid")
		require.Error(t, err)
		require.Equal(t, index*2, state.queryCalls)

		metadata, err := measuredStatement(role, parent, 0)
		require.NoError(t, err)
		rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT id")
		require.NoError(t, err)
		require.NoError(t, rows.Close())
		records, err := observer.Snapshot()
		require.NoError(t, err)
		require.Len(t, records, index*2+1)
		duplicate, err := measuredStatement(role, parent, 0)
		require.NoError(t, err)
		_, err = observer.QueryContext(t.Context(), database, duplicate, "duplicate")
		require.Error(t, err)
		skipped, err := measuredStatement(role, parent, 2)
		require.NoError(t, err)
		_, err = observer.QueryContext(t.Context(), database, skipped, "skipped")
		require.Error(t, err)
		require.Equal(t, index*2+1, state.queryCalls)
		next, err := measuredStatement(role, parent, 1)
		require.NoError(t, err)
		rows, err = observer.QueryContext(t.Context(), database, next, "SELECT id")
		require.NoError(t, err)
		require.NoError(t, rows.Close())
		require.Equal(t, (index+1)*2, state.queryCalls)
	}
}

func TestHandwrittenObserverNullableAndRawValues(t *testing.T) {
	when := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{
		columns: []string{"flag", "number", "text", "when", "nullable_when", "raw"},
		rows:    [][]driver.Value{{true, int64(9), "text", when, when, []byte("raw")}},
	}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "values", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT values")
	require.NoError(t, err)
	require.True(t, rows.Next())
	var flag sql.NullBool
	var number sql.NullInt64
	var text sql.NullString
	var gotWhen time.Time
	var nullableWhen sql.NullTime
	var raw sql.RawBytes
	require.NoError(t, rows.Scan(&flag, &number, &text, &gotWhen, &nullableWhen, &raw))
	require.True(t, flag.Valid)
	require.Equal(t, int64(9), number.Int64)
	require.Equal(t, "text", text.String)
	require.Equal(t, when, gotWhen)
	require.True(t, nullableWhen.Valid)
	require.Equal(t, when, nullableWhen.Time)
	require.Equal(t, sql.RawBytes([]byte("raw")), raw)
	require.False(t, rows.Next())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, [][]any{{true, int64(9), "text", when, when, sql.RawBytes([]byte("raw"))}}, records[0].RowValues)
}

func TestHandwrittenObserverScalarDestinationSnapshots(t *testing.T) {
	intValue := 4
	floatValue := 2.5
	stringValue := "value"
	boolValue := true
	bytesValue := []byte("bytes")
	timeValue := time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)
	cases := []struct {
		name        string
		destination any
		want        any
	}{
		{name: "bool", destination: &boolValue, want: true},
		{name: "int", destination: &intValue, want: 4},
		{name: "float", destination: &floatValue, want: 2.5},
		{name: "string", destination: &stringValue, want: "value"},
		{name: "bytes", destination: &bytesValue, want: []byte("bytes")},
		{name: "time", destination: &timeValue, want: timeValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := snapshotHandwrittenDestination(tc.destination)
			require.NoError(t, err)
			require.Equal(t, tc.want, value)
		})
	}
}

func TestHandwrittenObserverActualScalarScans(t *testing.T) {
	when := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	state := &handwrittenDriverState{queries: []handwrittenQueryResponse{{
		columns: []string{"flag", "number", "ratio", "text", "bytes", "when", "raw"},
		rows: [][]driver.Value{{
			true, int64(4), float64(2.5), "value", []byte("bytes"), when, []byte("raw"),
		}},
	}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleRead, "scalars", 0)
	require.NoError(t, err)
	rows, err := observer.QueryContext(t.Context(), database, metadata, "SELECT scalars", nil, int64(4), sql.Named("count", int64(5)))
	require.NoError(t, err)
	require.True(t, rows.Next())
	var flag bool
	var number int64
	var ratio float64
	var text string
	var bytesValue []byte
	var gotWhen time.Time
	var raw sql.RawBytes
	require.NoError(t, rows.Scan(&flag, &number, &ratio, &text, &bytesValue, &gotWhen, &raw))
	require.False(t, rows.Next())
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, int64(1), records[0].RowsConsumed)
	require.Equal(t, []any{nil, int64(4), sql.Named("count", int64(5))}, records[0].Args)
	require.Len(t, state.queryArgs, 1)
	require.Equal(t, "", state.queryArgs[0][0].Name)
	require.Equal(t, 1, state.queryArgs[0][0].Ordinal)
	require.Nil(t, state.queryArgs[0][0].Value)
	require.Equal(t, "", state.queryArgs[0][1].Name)
	require.Equal(t, 2, state.queryArgs[0][1].Ordinal)
	require.Equal(t, int64(4), state.queryArgs[0][1].Value)
	require.Equal(t, "count", state.queryArgs[0][2].Name)
	require.Equal(t, 3, state.queryArgs[0][2].Ordinal)
	require.Equal(t, int64(5), state.queryArgs[0][2].Value)
	require.Equal(t, [][]any{{true, int64(4), float64(2.5), "value", []byte("bytes"), when, sql.RawBytes([]byte("raw"))}}, records[0].RowValues)
	bytesValue[0] = 'B'
	raw[0] = 'R'
	records[0].RowValues[0][4].([]byte)[0] = 'S'
	records[0].RowValues[0][6].(sql.RawBytes)[0] = 'T'
	secondRecords, err := observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, []byte("bytes"), secondRecords[0].RowValues[0][4])
	require.Equal(t, sql.RawBytes([]byte("raw")), secondRecords[0].RowValues[0][6])
}

func TestHandwrittenObserverScalarExecArguments(t *testing.T) {
	state := &handwrittenDriverState{execs: []handwrittenExecResponse{{affected: 1}}}
	database := openHandwrittenDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	observer := newHandwrittenObserver()
	metadata, err := measuredStatement(roleMutation, "scalar-exec", 0)
	require.NoError(t, err)
	_, err = observer.ExecContext(t.Context(), database, metadata, "UPDATE values", nil, int64(4), sql.Named("count", int64(5)))
	require.NoError(t, err)
	records, err := observer.Snapshot()
	require.NoError(t, err)
	require.Equal(t, []any{nil, int64(4), sql.Named("count", int64(5))}, records[0].Args)
	require.Len(t, state.execArgs, 1)
	require.Equal(t, "", state.execArgs[0][0].Name)
	require.Equal(t, 1, state.execArgs[0][0].Ordinal)
	require.Nil(t, state.execArgs[0][0].Value)
	require.Equal(t, "", state.execArgs[0][1].Name)
	require.Equal(t, 2, state.execArgs[0][1].Ordinal)
	require.Equal(t, int64(4), state.execArgs[0][1].Value)
	require.Equal(t, "count", state.execArgs[0][2].Name)
	require.Equal(t, 3, state.execArgs[0][2].Ordinal)
	require.Equal(t, int64(5), state.execArgs[0][2].Value)
}

type handwrittenCanceledQuerier struct {
	calls     int
	statement string
	args      []any
	rows      *sql.Rows
}

func (q *handwrittenCanceledQuerier) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	q.calls++
	q.statement = statement
	q.args = args
	return q.rows, ctx.Err()
}
