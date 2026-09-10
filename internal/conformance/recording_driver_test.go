package conformance

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingCall struct {
	kind string
	sql  string
	args []any
}

type recordingResponse struct {
	Kind        string
	SQL         string
	Args        []any
	Columns     []string
	Rows        [][]driver.Value
	Affected    int64
	AffectedSet bool
	Err         error
	NextErr     error
}

type recordingDriverState struct {
	mu        sync.Mutex
	cols      []string
	rows      [][]driver.Value
	responses []recordingResponse
	strict    bool
	calls     []recordingCall
	closes    int
}

func (s *recordingDriverState) next(kind, query string, args []driver.NamedValue) (recordingResponse, error) {
	values := make([]any, len(args))
	for index, arg := range args {
		values[index] = arg.Value
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordingCall{kind: kind, sql: query, args: append([]any(nil), values...)})
	if len(s.responses) == 0 {
		if s.strict {
			return recordingResponse{}, fmt.Errorf("recording driver: unexpected extra %s %q", kind, query)
		}
		return recordingResponse{Kind: kind, SQL: query, Columns: append([]string(nil), s.cols...), Rows: cloneDriverRows(s.rows), Affected: 1}, nil
	}
	response := s.responses[0]
	if response.Kind != "" && response.Kind != kind {
		return recordingResponse{}, fmt.Errorf("recording driver: expected %s, got %s", response.Kind, kind)
	}
	if response.SQL != "" && response.SQL != query {
		return recordingResponse{}, fmt.Errorf("recording driver: expected SQL %q, got %q", response.SQL, query)
	}
	if response.Args != nil && !reflect.DeepEqual(response.Args, values) {
		return recordingResponse{}, fmt.Errorf("recording driver: expected args %#v, got %#v", response.Args, values)
	}
	s.responses = s.responses[1:]
	return response, nil
}

func (s *recordingDriverState) Calls() []recordingCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := append([]recordingCall(nil), s.calls...)
	for index := range result {
		result[index].args = append([]any(nil), result[index].args...)
	}
	return result
}

func (s *recordingDriverState) AssertDrained(t testing.TB) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Empty(t, s.responses, "recording driver responses remain")
}

type recordingConnector struct{ state *recordingDriverState }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	return &recordingConn{state: c.state}, nil
}

func (c recordingConnector) Driver() driver.Driver { return recordingDriver{} }

type recordingDriver struct{}

func (recordingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("recording driver requires sql.OpenDB")
}

type recordingConn struct{ state *recordingDriverState }

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return &recordingStmt{conn: c, query: query}, nil
}

func (c *recordingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &recordingStmt{conn: c, query: query, ctx: ctx}, nil
}

func (*recordingConn) Close() error              { return nil }
func (*recordingConn) Begin() (driver.Tx, error) { return recordingTx{}, nil }

func (c *recordingConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return recordingTx{}, nil
}

func (c *recordingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, err := c.state.next("query", query, args)
	if err != nil {
		return nil, err
	}
	if response.Err != nil {
		return nil, response.Err
	}
	columns := append([]string(nil), response.Columns...)
	rows := cloneDriverRows(response.Rows)
	if len(columns) == 0 {
		columns = append(columns, c.state.cols...)
	}
	if len(columns) == 0 && len(rows) != 0 {
		columns = make([]string, len(rows[0]))
		for index := range columns {
			columns[index] = fmt.Sprintf("column_%d", index)
		}
	}
	return &recordingRows{state: c.state, columns: columns, rows: rows, nextErr: response.NextErr}, nil
}

func (c *recordingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, err := c.state.next("exec", query, args)
	if err != nil {
		return nil, err
	}
	if response.Err != nil {
		return nil, response.Err
	}
	affected := response.Affected
	if !response.AffectedSet {
		affected = 1
	}
	return recordingResult(affected), nil
}

func (*recordingConn) CheckNamedValue(value *driver.NamedValue) error {
	converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
	if err != nil {
		return err
	}
	value.Value = converted
	return nil
}

type recordingStmt struct {
	conn  *recordingConn
	query string
	ctx   context.Context
}

func (s *recordingStmt) Close() error { return nil }
func (*recordingStmt) NumInput() int  { return -1 }

func (s *recordingStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}

func (s *recordingStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}

func (s *recordingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.ctx != nil {
		ctx = s.ctx
	}
	return s.conn.ExecContext(ctx, s.query, args)
}

func (s *recordingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s.ctx != nil {
		ctx = s.ctx
	}
	return s.conn.QueryContext(ctx, s.query, args)
}

func namedValues(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for index, arg := range args {
		values[index] = driver.NamedValue{Ordinal: index + 1, Value: arg}
	}
	return values
}

type recordingTx struct{}

func (recordingTx) Commit() error   { return nil }
func (recordingTx) Rollback() error { return nil }

type recordingResult int64

func (recordingResult) LastInsertId() (int64, error)        { return 0, nil }
func (result recordingResult) RowsAffected() (int64, error) { return int64(result), nil }

type recordingRows struct {
	state   *recordingDriverState
	columns []string
	rows    [][]driver.Value
	nextErr error
	index   int
	closed  bool
}

func (r *recordingRows) Columns() []string { return append([]string(nil), r.columns...) }

func (r *recordingRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.state.mu.Lock()
	r.state.closes++
	r.state.mu.Unlock()
	return nil
}

func (r *recordingRows) Next(destination []driver.Value) error {
	if r.nextErr != nil {
		err := r.nextErr
		r.nextErr = nil
		return err
	}
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(destination, r.rows[r.index])
	r.index++
	return nil
}

func cloneDriverRows(rows [][]driver.Value) [][]driver.Value {
	cloned := make([][]driver.Value, len(rows))
	for index, row := range rows {
		cloned[index] = append([]driver.Value(nil), row...)
	}
	return cloned
}

func openRecordingDB(state *recordingDriverState) *sql.DB {
	return sql.OpenDB(recordingConnector{state: state})
}

func TestRecordingDriverScriptsResponsesAndCopiesArgs(t *testing.T) {
	state := &recordingDriverState{
		strict: true,
		responses: []recordingResponse{
			{Kind: "query", SQL: "SELECT one", Args: []any{int64(1)}, Columns: []string{"id"}, Rows: [][]driver.Value{{int64(7)}}},
			{Kind: "exec", SQL: "UPDATE one", Args: []any{"ok"}, Affected: 3, AffectedSet: true},
		},
	}
	database := openRecordingDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	rows, err := database.QueryContext(t.Context(), "SELECT one", int64(1))
	require.NoError(t, err)
	var value int64
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&value))
	require.Equal(t, int64(7), value)
	require.NoError(t, rows.Close())
	result, err := database.ExecContext(t.Context(), "UPDATE one", "ok")
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(3), affected)
	state.AssertDrained(t)
	calls := state.Calls()
	require.Len(t, calls, 2)
	calls[0].args[0] = int64(99)
	require.Equal(t, int64(1), state.Calls()[0].args[0])
}

func TestRecordingDriverRejectsUnexpectedAndUnconsumedResponses(t *testing.T) {
	state := &recordingDriverState{strict: true, responses: []recordingResponse{{Kind: "query", SQL: "expected"}}}
	database := openRecordingDB(state)
	_, err := database.QueryContext(t.Context(), "wrong")
	require.Error(t, err)
	require.Len(t, state.responses, 1)

	state = &recordingDriverState{strict: true, responses: []recordingResponse{{Kind: "exec", SQL: "left"}}}
	database = openRecordingDB(state)
	_, err = database.ExecContext(t.Context(), "other")
	require.Error(t, err)
	require.Len(t, state.responses, 1)
}

func TestRecordingDriverContextAndRowsErrorsCloseOnce(t *testing.T) {
	state := &recordingDriverState{strict: true, responses: []recordingResponse{
		{Kind: "query", SQL: "next", Columns: []string{"id"}, NextErr: errors.New("next failed")},
		{Kind: "query", SQL: "early", Columns: []string{"id"}, Rows: [][]driver.Value{{int64(1), int64(2)}}},
	}}
	database := openRecordingDB(state)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := database.QueryContext(ctx, "next")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, state.Calls())

	rows, err := database.QueryContext(t.Context(), "next")
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.Error(t, rows.Err())
	require.NoError(t, rows.Close())
	require.NoError(t, rows.Close())
	require.Equal(t, 1, state.closes)

	early, err := database.QueryContext(t.Context(), "early")
	require.NoError(t, err)
	require.NoError(t, early.Close())
	require.NoError(t, early.Close())
	require.Equal(t, 2, state.closes)
	require.Empty(t, state.responses)
}

func TestRecordingDriverResponseErrorsAndNaturalEOF(t *testing.T) {
	queryErr := errors.New("query failed")
	execErr := errors.New("exec failed")
	state := &recordingDriverState{strict: true, responses: []recordingResponse{
		{Kind: "query", SQL: "query error", Err: queryErr},
		{Kind: "exec", SQL: "exec error", Err: execErr},
		{Kind: "query", SQL: "natural eof", Columns: []string{"id"}, Rows: [][]driver.Value{{int64(1)}}},
	}}
	database := openRecordingDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err := database.QueryContext(t.Context(), "query error")
	require.ErrorIs(t, err, queryErr)
	_, err = database.ExecContext(t.Context(), "exec error")
	require.ErrorIs(t, err, execErr)
	rows, err := database.QueryContext(t.Context(), "natural eof")
	require.NoError(t, err)
	require.True(t, rows.Next())
	var id int64
	require.NoError(t, rows.Scan(&id))
	require.Equal(t, int64(1), id)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	require.Equal(t, 1, state.closes)
	state.AssertDrained(t)
}
