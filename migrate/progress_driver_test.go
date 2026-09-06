package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

const recoveryDriverName = "rasql-p7-recovery"

var recoveryCurrent atomic.Pointer[recoveryState]

type recoveryState struct {
	mu                                                            sync.Mutex
	history                                                       map[string]string
	progress                                                      *recoveryProgress
	effects                                                       map[string]bool
	executions                                                    map[string]int
	failIntent, failSQL, failCheckpoint, failHistory, failCleanup bool
	nextConn                                                      atomic.Int64
}
type recoveryProgress struct {
	id, checksum, direction, source string
	sourceIndex, nextIndex          int
}
type recoveryDriver struct{}
type recoveryConn struct {
	state *recoveryState
	id    int64
}

func init() { sql.Register(recoveryDriverName, recoveryDriver{}) }
func (recoveryDriver) Open(string) (driver.Conn, error) {
	s := recoveryCurrent.Load()
	return &recoveryConn{s, s.nextConn.Add(1)}, nil
}
func (c *recoveryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *recoveryConn) Close() error              { return nil }
func (c *recoveryConn) Begin() (driver.Tx, error) { return nil, errors.New("transactions unsupported") }

func (c *recoveryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS") {
		s.executions[query]++
		return driver.RowsAffected(0), nil
	}
	if strings.Contains(query, "FAIL_SOURCE") {
		s.executions[query]++
		if s.failSQL {
			s.failSQL = false
			return nil, errors.New("source failed")
		}
	}
	if strings.HasPrefix(query, "CREATE TABLE") || strings.HasPrefix(query, "DROP TABLE") {
		s.executions[query]++
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(query, "INSERT INTO") && strings.Contains(query, "progress") {
		if s.failIntent {
			s.failIntent = false
			return nil, errors.New("intent failed")
		}
		s.progress = &recoveryProgress{stringArg(args, 0), stringArg(args, 1), stringArg(args, 2), stringArg(args, 4), intArg(args, 3), intArg(args, 5)}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "UPDATE") && strings.Contains(query, "progress") {
		if s.failCheckpoint {
			s.failCheckpoint = false
			return nil, errors.New("checkpoint failed")
		}
		if s.progress != nil {
			s.progress.nextIndex = intArg(args, 0)
			s.progress.source = stringArg(args, 1)
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "DELETE") && strings.Contains(query, "progress") {
		if s.failCleanup {
			s.failCleanup = false
			return nil, errors.New("cleanup failed")
		}
		s.progress = nil
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "INSERT INTO") && strings.Contains(query, "rasql_schema_migrations`") && !strings.Contains(query, "progress") {
		if s.failHistory {
			s.failHistory = false
			return nil, errors.New("history failed")
		}
		s.history[stringArg(args, 0)] = stringArg(args, 1)
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "DELETE") && strings.Contains(query, "rasql_schema_migrations`") && !strings.Contains(query, "progress") {
		if s.failHistory {
			s.failHistory = false
			return nil, errors.New("history failed")
		}
		delete(s.history, stringArg(args, 0))
		return driver.RowsAffected(1), nil
	}
	return driver.RowsAffected(0), nil
}

func TestRecoveryDriverApplyFailureWindowsRetainExactRows(t *testing.T) {
	for _, failure := range []struct {
		name     string
		set      func(*recoveryState)
		wantNext int
	}{
		{name: "intent", set: func(s *recoveryState) { s.failIntent = true }, wantNext: -1},
		{name: "sql", set: func(s *recoveryState) { s.failSQL = true }, wantNext: 0},
		{name: "checkpoint", set: func(s *recoveryState) { s.failCheckpoint = true }, wantNext: 0},
		{name: "history", set: func(s *recoveryState) { s.failHistory = true }, wantNext: 1},
		{name: "cleanup", set: func(s *recoveryState) { s.failCleanup = true }, wantNext: 1},
	} {
		t.Run(failure.name, func(t *testing.T) {
			state := &recoveryState{history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{}}
			failure.set(state)
			recoveryCurrent.Store(state)
			db, err := sql.Open(recoveryDriverName, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			runner, err := New(db, dialect.MySQL())
			require.NoError(t, err)
			sourceSQL := "CREATE TABLE one"
			if failure.name == "sql" {
				sourceSQL = "CREATE TABLE FAIL_SOURCE"
			}
			migration := Migration{ID: "001", Statements: []Statement{{Source: "one.sql", SQL: sqltext.Text(sourceSQL)}}}
			_, err = runner.Apply(t.Context(), AllPending(), migration)
			require.Error(t, err)
			state.mu.Lock()
			if failure.wantNext < 0 {
				require.Nil(t, state.progress)
			} else {
				require.NotNil(t, state.progress)
				require.Equal(t, failure.wantNext, state.progress.nextIndex)
			}
			state.mu.Unlock()
		})
	}
}

func TestRecoveryDriverRevertFailureWindowsRetainExactRows(t *testing.T) {
	for _, failure := range []struct {
		name     string
		set      func(*recoveryState)
		wantNext int
	}{
		{name: "intent", set: func(s *recoveryState) { s.failIntent = true }, wantNext: -1},
		{name: "sql", set: func(s *recoveryState) { s.failSQL = true }, wantNext: 0},
		{name: "checkpoint", set: func(s *recoveryState) { s.failCheckpoint = true }, wantNext: 0},
		{name: "history", set: func(s *recoveryState) { s.failHistory = true }, wantNext: 1},
		{name: "cleanup", set: func(s *recoveryState) { s.failCleanup = true }, wantNext: 1},
	} {
		t.Run(failure.name, func(t *testing.T) {
			state := &recoveryState{history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{}}
			migration := Migration{ID: "001", Statements: []Statement{{Source: "one.sql", SQL: sqltext.Text("CREATE TABLE one")}}, Down: []Statement{{Source: "one.down.sql", SQL: sqltext.Text("DROP TABLE one FAIL_SOURCE")}}}
			state.history[migration.ID] = checksum(migration.Statements)
			failure.set(state)
			recoveryCurrent.Store(state)
			db, err := sql.Open(recoveryDriverName, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			runner, err := New(db, dialect.MySQL())
			require.NoError(t, err)
			_, err = runner.Revert(t.Context(), Steps(1), migration)
			require.Error(t, err)
			state.mu.Lock()
			if failure.wantNext < 0 {
				require.Nil(t, state.progress)
			} else {
				require.NotNil(t, state.progress)
				require.Equal(t, failure.wantNext, state.progress.nextIndex)
			}
			state.mu.Unlock()
		})
	}
}

func (c *recoveryConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(query, "SELECT GET_LOCK"):
		return recoveryRows([]string{"acquired"}, int64(1)), nil
	case strings.HasPrefix(query, "SELECT RELEASE_LOCK"):
		return recoveryRows([]string{"released"}, int64(1)), nil
	case strings.Contains(query, "FROM `rasql_schema_migrations_progress`"):
		if s.progress == nil {
			return recoveryRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}), nil
		}
		p := s.progress
		return recoveryRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}, p.id, p.checksum, p.direction, int64(p.sourceIndex), p.source, int64(p.nextIndex)), nil
	case strings.Contains(query, "FROM `rasql_schema_migrations`"):
		values := make([][]driver.Value, 0, len(s.history))
		for id, sum := range s.history {
			values = append(values, []driver.Value{id, sum})
		}
		rows := &recoveryRowsImpl{columns: []string{"id", "checksum"}, values: values}
		return rows, nil
	default:
		return recoveryRows([]string{"value"}), nil
	}
}
func stringArg(args []driver.NamedValue, i int) string { return args[i].Value.(string) }
func intArg(args []driver.NamedValue, i int) int {
	switch v := args[i].Value.(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}
func recoveryRows(columns []string, values ...driver.Value) driver.Rows {
	var all [][]driver.Value
	if len(values) > 0 {
		all = [][]driver.Value{values}
	}
	return &recoveryRowsImpl{columns: columns, values: all}
}

type recoveryRowsImpl struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *recoveryRowsImpl) Columns() []string { return r.columns }
func (r *recoveryRowsImpl) Close() error      { return nil }
func (r *recoveryRowsImpl) Next(v []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(v, r.values[r.index])
	r.index++
	return nil
}

func TestRecoveryDriverApplyRestartAndReconcile(t *testing.T) {
	state := &recoveryState{history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{}, failSQL: true}
	recoveryCurrent.Store(state)
	db, err := sql.Open(recoveryDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runner, err := New(db, dialect.MySQL())
	require.NoError(t, err)
	migration := Migration{ID: "001", Statements: []Statement{{Source: "one.sql", SQL: sqltext.Text("CREATE TABLE one")}, {Source: "two.sql", SQL: sqltext.Text("FAIL_SOURCE")}}}
	result, err := runner.ApplyResult(t.Context(), AllPending(), migration)
	require.Error(t, err)
	require.NotNil(t, result.Incomplete)
	require.Equal(t, "two.sql", result.Incomplete.Source)
	_, err = runner.ApplyPlan(t.Context(), AllPending(), migration)
	require.Error(t, err)
	state.mu.Lock()
	require.NotNil(t, state.progress)
	require.Equal(t, 1, state.progress.sourceIndex)
	require.Equal(t, 1, state.progress.nextIndex)
	state.mu.Unlock()
	restarted, err := sql.Open(recoveryDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = restarted.Close() })
	runner, err = New(restarted, dialect.MySQL())
	require.NoError(t, err)
	check := recoveryCheck{executed: true}
	require.NoError(t, runner.Reconcile(t.Context(), &check, migration))
	require.Equal(t, ReconcileExecuted, check.decision)
	result, err = runner.ApplyResult(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Empty(t, result.Completed)
	state.mu.Lock()
	require.Equal(t, 1, state.executions["CREATE TABLE one"])
	state.mu.Unlock()
}

func TestRecoveryDriverReconcileNotExecutedRetainsPriorCheckpoint(t *testing.T) {
	state := &recoveryState{history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{}, failSQL: true}
	recoveryCurrent.Store(state)
	db, err := sql.Open(recoveryDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runner, err := New(db, dialect.MySQL())
	require.NoError(t, err)
	migration := Migration{ID: "001", Statements: []Statement{{Source: "one.sql", SQL: sqltext.Text("CREATE TABLE one")}, {Source: "two.sql", SQL: sqltext.Text("CREATE TABLE FAIL_SOURCE")}}}
	_, err = runner.Apply(t.Context(), AllPending(), migration)
	require.Error(t, err)
	check := recoveryCheck{}
	require.NoError(t, runner.Reconcile(t.Context(), &check, migration))
	state.mu.Lock()
	require.NotNil(t, state.progress)
	require.Equal(t, 0, state.progress.sourceIndex)
	require.Equal(t, 1, state.progress.nextIndex)
	state.mu.Unlock()
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, completed, 1)
	state.mu.Lock()
	require.Equal(t, 1, state.executions["CREATE TABLE one"])
	state.mu.Unlock()
}

type recoveryCheck struct {
	executed bool
	decision ReconcileDecision
}

func (c *recoveryCheck) Check(context.Context, *sql.Conn, IncompleteMigration) (ReconcileDecision, error) {
	if c.executed {
		c.decision = ReconcileExecuted
		return c.decision, nil
	}
	c.decision = ReconcileNotExecuted
	return c.decision, nil
}
