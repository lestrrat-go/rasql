package dbtest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// Recovery is a persistent, cooperative MySQL-shaped database fixture. Each
// instance owns its driver name and state, so independent tests can run in
// parallel without sharing a global current fixture.
type Recovery struct {
	name  string
	state *recoveryState
}

type Failure uint8

const (
	FailIntent Failure = iota + 1
	FailMigrationSQL
	FailCheckpoint
	FailHistory
	FailProgressCleanup
)

type Progress struct {
	ID, Checksum, Direction, Source string
	SourceIndex, NextIndex          int
}

type Snapshot struct {
	History                    map[string]string
	Progress                   *Progress
	Effects                    map[string]bool
	Executions                 map[string]int
	Connections                int64
	LockOwner                  int64
	LockAcquires, LockReleases int
}

var recoveryID atomic.Uint64
var recoveryStates sync.Map

type recoveryState struct {
	mu                         sync.Mutex
	history                    map[string]string
	progress                   *Progress
	effects                    map[string]bool
	executions                 map[string]int
	failure                    Failure
	failSQLAt                  int
	sqlIndex                   int
	failCheckpointAt           int
	checkpointIndex            int
	nextConn                   int64
	lockOwner                  int64
	lockAcquires, lockReleases int
}

// NewRecovery creates an empty isolated fixture.
func NewRecovery() *Recovery {
	name := fmt.Sprintf("rasql-p7-recovery-%d", recoveryID.Add(1))
	fixture := &Recovery{name: name, state: &recoveryState{
		history: make(map[string]string), effects: make(map[string]bool), executions: make(map[string]int),
		failSQLAt:        -1,
		failCheckpointAt: 0,
	}}
	recoveryStates.Store(name, fixture.state)
	sql.Register(name, recoveryDriver{})
	return fixture
}

// Open returns a database handle backed by this fixture. Closing it and
// calling Open again preserves all history, progress, effects, and counts.
func (r *Recovery) Open() (*sql.DB, error) { return sql.Open(r.name, r.name) }

// DSN returns the fixture's driver name and unique data source name for
// callers that replace a package's database opener.
func (r *Recovery) DSN() string { return r.name }

// FailNext injects one failure at the selected journal or migration step.
func (r *Recovery) FailNext(failure Failure) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.failure = failure
	if failure == FailMigrationSQL {
		r.state.failSQLAt = 0
		r.state.sqlIndex = 0
	}
	if failure == FailCheckpoint {
		r.state.failCheckpointAt = 0
		r.state.checkpointIndex = 0
	}
}

// FailMigrationAt injects one SQL failure at the zero-based source index.
func (r *Recovery) FailMigrationAt(index int) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.failure = FailMigrationSQL
	r.state.failSQLAt = index
	r.state.sqlIndex = 0
}

// FailCheckpointAt injects a failure after the selected source's SQL has run.
func (r *Recovery) FailCheckpointAt(index int) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.failure = FailCheckpoint
	r.state.failCheckpointAt = index
	r.state.checkpointIndex = 0
}

// SetProgress installs a durable row for observer and reconciliation tests.
func (r *Recovery) SetProgress(value Progress) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	copy := value
	r.state.progress = &copy
}

// SetHistory installs a durable completed migration record.
func (r *Recovery) SetHistory(id, checksum string) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.history[id] = checksum
}

// Snapshot returns defensive copies of all fixture state.
func (r *Recovery) Snapshot() Snapshot {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	result := Snapshot{
		History: make(map[string]string, len(r.state.history)), Effects: make(map[string]bool, len(r.state.effects)),
		Executions: make(map[string]int, len(r.state.executions)), Connections: r.state.nextConn,
		LockOwner: r.state.lockOwner, LockAcquires: r.state.lockAcquires, LockReleases: r.state.lockReleases,
	}
	for key, value := range r.state.history {
		result.History[key] = value
	}
	for key, value := range r.state.effects {
		result.Effects[key] = value
	}
	for key, value := range r.state.executions {
		result.Executions[key] = value
	}
	if r.state.progress != nil {
		value := *r.state.progress
		result.Progress = &value
	}
	return result
}

type recoveryDriver struct{}
type recoveryConn struct {
	state *recoveryState
	id    int64
}

func (recoveryDriver) Open(name string) (driver.Conn, error) {
	value, ok := recoveryStates.Load(name)
	if !ok {
		return nil, errors.New("recovery fixture is not registered")
	}
	state := value.(*recoveryState)
	state.mu.Lock()
	state.nextConn++
	id := state.nextConn
	state.mu.Unlock()
	return &recoveryConn{state: state, id: id}, nil
}
func (*recoveryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (*recoveryConn) Close() error              { return nil }
func (*recoveryConn) Begin() (driver.Tx, error) { return nil, errors.New("transactions unsupported") }
func (*recoveryConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return recoveryTx{}, nil
}

type recoveryTx struct{}

func (recoveryTx) Commit() error   { return nil }
func (recoveryTx) Rollback() error { return nil }

func (c *recoveryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	q := strings.ToUpper(strings.TrimSpace(query))
	if strings.Contains(q, "GET_LOCK") {
		c.state.lockOwner = c.id
		c.state.lockAcquires++
		return result(1), nil
	}
	if strings.Contains(q, "RELEASE_LOCK") {
		c.state.lockOwner = 0
		c.state.lockReleases++
		return result(1), nil
	}
	if strings.Contains(q, "CREATE TABLE IF NOT EXISTS") {
		return result(0), nil
	}
	if strings.Contains(q, "INSERT INTO `RASQL_SCHEMA_MIGRATIONS_PROGRESS`") {
		if c.state.failure == FailIntent {
			c.state.failure = 0
			return nil, errors.New("intent failure")
		}
		c.state.progress = &Progress{ID: stringArg(args, 0), Checksum: stringArg(args, 1), Direction: stringArg(args, 2), SourceIndex: intArg(args, 3), Source: stringArg(args, 4), NextIndex: intArg(args, 5)}
		return result(1), nil
	}
	if strings.HasPrefix(q, "UPDATE") && strings.Contains(q, "RASQL_SCHEMA_MIGRATIONS_PROGRESS") {
		index := c.state.checkpointIndex
		c.state.checkpointIndex++
		if c.state.failure == FailCheckpoint && index == c.state.failCheckpointAt {
			c.state.failure = 0
			return nil, errors.New("checkpoint failure")
		}
		if c.state.progress != nil {
			c.state.progress.NextIndex = intArg(args, 0)
			c.state.progress.Source = stringArg(args, 1)
		}
		return result(1), nil
	}
	if strings.HasPrefix(q, "DELETE") && strings.Contains(q, "RASQL_SCHEMA_MIGRATIONS_PROGRESS") {
		if c.state.failure == FailProgressCleanup {
			c.state.failure = 0
			return nil, errors.New("progress cleanup failure")
		}
		c.state.progress = nil
		return result(1), nil
	}
	if strings.HasPrefix(q, "INSERT INTO") && strings.Contains(q, "RASQL_SCHEMA_MIGRATIONS`") {
		if c.state.failure == FailHistory {
			c.state.failure = 0
			return nil, errors.New("history failure")
		}
		c.state.history[stringArg(args, 0)] = stringArg(args, 1)
		return result(1), nil
	}
	if strings.HasPrefix(q, "DELETE") && strings.Contains(q, "RASQL_SCHEMA_MIGRATIONS`") {
		if c.state.failure == FailHistory {
			c.state.failure = 0
			return nil, errors.New("history failure")
		}
		delete(c.state.history, stringArg(args, 0))
		return result(1), nil
	}
	if c.state.failure == FailMigrationSQL {
		index := c.state.sqlIndex
		c.state.sqlIndex++
		if index == c.state.failSQLAt {
			c.state.failure = 0
			return nil, errors.New("migration SQL failure")
		}
	}
	c.state.effects[query] = true
	c.state.executions[query]++
	return result(1), nil
}

func (c *recoveryConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	q := strings.ToUpper(strings.TrimSpace(query))
	switch {
	case strings.Contains(q, "CONNECTION_ID"):
		return rows([]string{"connection_id"}, c.id), nil
	case strings.Contains(q, "GET_LOCK"):
		c.state.lockOwner = c.id
		c.state.lockAcquires++
		return rows([]string{"acquired"}, int64(1)), nil
	case strings.Contains(q, "RELEASE_LOCK"):
		c.state.lockOwner = 0
		c.state.lockReleases++
		return rows([]string{"released"}, int64(1)), nil
	case strings.Contains(q, "FROM `RASQL_SCHEMA_MIGRATIONS_PROGRESS`"):
		if c.state.progress == nil {
			return rows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}), nil
		}
		p := c.state.progress
		return rows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}, p.ID, p.Checksum, p.Direction, int64(p.SourceIndex), p.Source, int64(p.NextIndex)), nil
	case strings.Contains(q, "FROM `RASQL_SCHEMA_MIGRATIONS`"):
		values := make([][]driver.Value, 0, len(c.state.history))
		for id, sum := range c.state.history {
			values = append(values, []driver.Value{id, sum})
		}
		return &recoveryRows{columns: []string{"id", "checksum"}, values: values}, nil
	default:
		return rows([]string{"value"}), nil
	}
}

type recoveryResult int64

func (r recoveryResult) LastInsertId() (int64, error) { return 0, nil }
func (r recoveryResult) RowsAffected() (int64, error) { return int64(r), nil }
func result(value int64) driver.Result                { return recoveryResult(value) }

type recoveryRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *recoveryRows) Columns() []string { return r.columns }
func (*recoveryRows) Close() error        { return nil }
func (r *recoveryRows) Next(value []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(value, r.values[r.index])
	r.index++
	return nil
}
func rows(columns []string, values ...driver.Value) driver.Rows {
	var all [][]driver.Value
	if len(values) > 0 {
		all = [][]driver.Value{values}
	}
	return &recoveryRows{columns: columns, values: all}
}

func stringArg(args []driver.NamedValue, index int) string { return fmt.Sprint(args[index].Value) }
func intArg(args []driver.NamedValue, index int) int {
	switch value := args[index].Value.(type) {
	case int64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}
