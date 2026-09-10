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
	queryDelay   time.Duration
	nextDelay    time.Duration
	queryStarted chan<- struct{}
	queryRelease <-chan struct{}
	nextStarted  chan<- struct{}
	nextRelease  <-chan struct{}
	columnError  error
}

type lifecycleDriverRecorder struct {
	mu            sync.Mutex
	markers       []string
	queryStarted  time.Time
	queryFinished time.Time
	nextStarted   time.Time
	nextFinished  time.Time
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
	if c.recorder != nil {
		c.recorder.markQueryStarted()
	}
	if c.config.queryStarted != nil {
		c.config.queryStarted <- struct{}{}
	}
	if c.config.queryDelay > 0 {
		time.Sleep(c.config.queryDelay)
	}
	if c.config.queryRelease != nil {
		<-c.config.queryRelease
	}
	if c.recorder != nil {
		c.recorder.markQueryFinished()
	}
	if c.recorder != nil {
		if marker, ok := ctx.Value(lifecycleMarkerKey{}).(string); ok {
			c.recorder.mu.Lock()
			c.recorder.markers = append(c.recorder.markers, marker)
			c.recorder.mu.Unlock()
		}
	}
	return &lifecycleRows{config: c.config, recorder: c.recorder}, nil
}

func (c lifecycleConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type lifecycleRows struct {
	config   lifecycleDriverConfig
	recorder *lifecycleDriverRecorder
	index    int
}

func (r *lifecycleRows) Columns() []string {
	if r.config.columnError != nil {
		return nil
	}
	return []string{"value"}
}

func (r *lifecycleRows) Close() error { return nil }

func (r *lifecycleRows) Next(destinations []driver.Value) error {
	if r.recorder != nil {
		r.recorder.markNextStarted()
	}
	if r.config.nextStarted != nil {
		r.config.nextStarted <- struct{}{}
	}
	if r.config.nextDelay > 0 {
		time.Sleep(r.config.nextDelay)
	}
	if r.config.nextRelease != nil {
		<-r.config.nextRelease
	}
	if r.recorder != nil {
		r.recorder.markNextFinished()
	}
	if r.index > 0 {
		return io.EOF
	}
	r.index++
	destinations[0] = int64(1)
	return nil
}

func (r *lifecycleDriverRecorder) markQueryStarted() {
	r.mu.Lock()
	r.queryStarted = time.Now()
	r.mu.Unlock()
}

func (r *lifecycleDriverRecorder) markQueryFinished() {
	r.mu.Lock()
	r.queryFinished = time.Now()
	r.mu.Unlock()
}

func (r *lifecycleDriverRecorder) markNextStarted() {
	r.mu.Lock()
	r.nextStarted = time.Now()
	r.mu.Unlock()
}

func (r *lifecycleDriverRecorder) markNextFinished() {
	r.mu.Lock()
	r.nextFinished = time.Now()
	r.mu.Unlock()
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
