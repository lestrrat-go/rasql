package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

const secT2DriverName = "rasql-sec-t2-cooperative"

var secT2CurrentState atomic.Pointer[secT2State]

func init() { sql.Register(secT2DriverName, secT2Driver{}) }

type secT2State struct {
	historyStarted chan struct{}
	releaseMode    string
	nextID         atomic.Int64
}

type secT2Driver struct{}

func (secT2Driver) Open(string) (driver.Conn, error) {
	state := secT2CurrentState.Load()
	connection := &secT2Conn{state: state, id: state.nextID.Add(1)}
	return connection, nil
}

type secT2Conn struct {
	state *secT2State
	id    int64
}

func (c *secT2Conn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (secT2Conn) Close() error              { return nil }
func (secT2Conn) Begin() (driver.Tx, error) { return nil, errors.New("transactions are not supported") }

func (c *secT2Conn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS") {
		select {
		case <-c.state.historyStarted:
		default:
			close(c.state.historyStarted)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return driver.RowsAffected(0), nil
}

func (c *secT2Conn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.HasPrefix(query, "SELECT GET_LOCK"):
		return &secT2Rows{columns: []string{"acquired"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.HasPrefix(query, "SELECT RELEASE_LOCK"):
		switch c.state.releaseMode {
		case "blocked":
			<-ctx.Done()
			return nil, ctx.Err()
		case "error":
			return nil, errSecT2Release
		case "bad":
			return nil, driver.ErrBadConn
		case "zero":
			return &secT2Rows{columns: []string{"released"}, values: [][]driver.Value{{int64(0)}}}, nil
		default:
			return &secT2Rows{columns: []string{"released"}, values: [][]driver.Value{{int64(1)}}}, nil
		}
	case strings.HasPrefix(query, "SELECT PHYSICAL_ID"):
		return &secT2Rows{columns: []string{"id"}, values: [][]driver.Value{{c.id}}}, nil
	default:
		return &secT2Rows{columns: []string{"id"}}, nil
	}
}

type secT2Rows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *secT2Rows) Columns() []string { return r.columns }
func (r *secT2Rows) Close() error      { return nil }
func (r *secT2Rows) Next(values []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.index])
	r.index++
	return nil
}

var (
	errSecT2Operation = errors.New("operation canceled")
	errSecT2Release   = errors.New("release failed")
)

func TestPublicMySQLCancellationBoundsCleanup(t *testing.T) {
	for _, operation := range []string{"apply", "revert"} {
		t.Run(operation, func(t *testing.T) {
			state := &secT2State{historyStarted: make(chan struct{}), releaseMode: "blocked"}
			secT2CurrentState.Store(state)
			database, err := sql.Open(secT2DriverName, "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			runner, err := New(database, dialect.MySQL())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan error, 1)
			go func() {
				if operation == "apply" {
					_, err = runner.Apply(ctx, AllPending())
				} else {
					_, err = runner.Revert(ctx, Steps(1))
				}
				started <- err
			}()
			<-state.historyStarted
			cancel()
			begin := time.Now()
			err = <-started
			require.ErrorIs(t, err, context.Canceled)
			require.GreaterOrEqual(t, time.Since(begin), 5*time.Second)
			require.Less(t, time.Since(begin), 7*time.Second)
		})
	}
}

func TestMySQLLockCleanupRetiresUncertainSessions(t *testing.T) {
	for _, mode := range []string{"zero", "error"} {
		t.Run(mode, func(t *testing.T) {
			state := &secT2State{historyStarted: make(chan struct{}), releaseMode: mode}
			secT2CurrentState.Store(state)
			database, err := sql.Open(secT2DriverName, "")
			require.NoError(t, err)
			database.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = database.Close() })
			runner, err := New(database, dialect.MySQL())
			require.NoError(t, err)
			connection, err := database.Conn(t.Context())
			require.NoError(t, err)
			var originalID int64
			require.NoError(t, connection.QueryRowContext(t.Context(), "SELECT PHYSICAL_ID").Scan(&originalID))
			_, err = runner.withMySQLLock(t.Context(), connection, func() ([]Migration, error) { return nil, nil })
			require.Error(t, err)
			_ = connection.Close()
			var replacementID int64
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT PHYSICAL_ID").Scan(&replacementID))
			require.NotEqual(t, originalID, replacementID)
		})
	}
}

func TestMySQLLockCleanupRetainsCertainSession(t *testing.T) {
	state := &secT2State{historyStarted: make(chan struct{}), releaseMode: "one"}
	secT2CurrentState.Store(state)
	database, err := sql.Open(secT2DriverName, "")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	var originalID int64
	require.NoError(t, connection.QueryRowContext(t.Context(), "SELECT PHYSICAL_ID").Scan(&originalID))
	_, err = runner.withMySQLLock(t.Context(), connection, func() ([]Migration, error) { return nil, nil })
	require.NoError(t, err)
	_ = connection.Close()
	var retainedID int64
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT PHYSICAL_ID").Scan(&retainedID))
	require.Equal(t, originalID, retainedID)
}

func TestMySQLLockCleanupPreservesMigrationsAndRawFailure(t *testing.T) {
	state := &secT2State{historyStarted: make(chan struct{}), releaseMode: "bad"}
	secT2CurrentState.Store(state)
	database, err := sql.Open(secT2DriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	operationErr := errSecT2Operation
	expected := []Migration{{ID: "001"}}
	got, err := runner.withMySQLLock(t.Context(), connection, func() ([]Migration, error) { return expected, operationErr })
	require.Equal(t, expected, got)
	require.ErrorIs(t, err, operationErr)
	require.ErrorIs(t, err, driver.ErrBadConn)
	require.ErrorContains(t, err, "could not mark MySQL migration connection bad")

	state.releaseMode = "error"
	connection, err = database.Conn(t.Context())
	require.NoError(t, err)
	got, err = runner.withMySQLLock(t.Context(), connection, func() ([]Migration, error) { return expected, operationErr })
	require.Equal(t, expected, got)
	require.ErrorIs(t, err, operationErr)
	require.ErrorIs(t, err, errSecT2Release)
}
