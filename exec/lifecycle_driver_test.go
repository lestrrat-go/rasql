package exec_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"time"
)

type lifecycleDriverConfig struct {
	queryDelay  time.Duration
	nextDelay   time.Duration
	columnError error
}

type lifecycleDriverRecorder struct {
	mu      sync.Mutex
	markers []string
}

type lifecycleConnector struct {
	config   lifecycleDriverConfig
	recorder *lifecycleDriverRecorder
}

func (c lifecycleConnector) Connect(context.Context) (driver.Conn, error) {
	return lifecycleConn(c), nil
}

func (c lifecycleConnector) Driver() driver.Driver { return lifecycleDriver{} }

type lifecycleDriver struct{}

func (lifecycleDriver) Open(string) (driver.Conn, error) { return lifecycleConn{}, nil }

type lifecycleConn struct {
	config   lifecycleDriverConfig
	recorder *lifecycleDriverRecorder
}

func (lifecycleConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (lifecycleConn) Close() error              { return nil }
func (lifecycleConn) Begin() (driver.Tx, error) { return nil, errors.New("begin is not supported") }

func (c lifecycleConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.config.queryDelay > 0 {
		time.Sleep(c.config.queryDelay)
	}
	if c.recorder != nil {
		if marker, ok := ctx.Value(lifecycleMarkerKey{}).(string); ok {
			c.recorder.mu.Lock()
			c.recorder.markers = append(c.recorder.markers, marker)
			c.recorder.mu.Unlock()
		}
	}
	return &lifecycleRows{config: c.config}, nil
}

func (c lifecycleConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type lifecycleRows struct {
	config lifecycleDriverConfig
	index  int
}

func (r *lifecycleRows) Columns() []string {
	if r.config.columnError != nil {
		return nil
	}
	return []string{"value"}
}

func (r *lifecycleRows) Close() error { return nil }

func (r *lifecycleRows) Next(destinations []driver.Value) error {
	if r.config.nextDelay > 0 {
		time.Sleep(r.config.nextDelay)
	}
	if r.index > 0 {
		return io.EOF
	}
	r.index++
	destinations[0] = int64(1)
	return nil
}

func openLifecycleDatabase(t interface {
	Cleanup(func())
	Helper()
}, config lifecycleDriverConfig, recorder *lifecycleDriverRecorder) *sql.DB {
	t.Helper()
	database := sql.OpenDB(lifecycleConnector{config: config, recorder: recorder})
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(4)
	return database
}

type lifecycleMarkerKey struct{}
