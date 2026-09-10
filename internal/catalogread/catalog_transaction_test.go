package catalogread_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
)

type transactionProbe struct {
	mu        sync.Mutex
	opts      *sql.TxOptions
	queryErr  error
	block     bool
	started   chan struct{}
	commitErr error
	rolled    bool
	committed bool
	rolledCh  chan struct{}
}

type probeDriver struct{ probe *transactionProbe }
type probeConn struct{ probe *transactionProbe }
type probeTx struct{ probe *transactionProbe }

func (d probeDriver) Open(string) (driver.Conn, error) { return &probeConn{probe: d.probe}, nil }
func (c *probeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *probeConn) Close() error { return nil }
func (c *probeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *probeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.probe.mu.Lock()
	c.probe.opts = &sql.TxOptions{Isolation: sql.IsolationLevel(opts.Isolation), ReadOnly: opts.ReadOnly}
	c.probe.mu.Unlock()
	return &probeTx{probe: c.probe}, nil
}
func (c *probeConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.probe.block {
		close(c.probe.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, c.probe.queryErr
}

func TestReadCancellationRollsBackWithoutPartialResult(t *testing.T) {
	rolled := make(chan struct{})
	probe := &transactionProbe{block: true, started: make(chan struct{}), rolledCh: rolled}
	db := openProbe(t, probe)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		result catalogread.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := catalogread.Read(ctx, db, sqliteProfile(t), catalogread.Scope{})
		done <- outcome{result: result, err: err}
	}()
	<-probe.started
	cancel()
	got := <-done
	require.ErrorIs(t, got.err, context.Canceled)
	require.Empty(t, got.result.Tables)
	<-rolled
	probe.mu.Lock()
	require.True(t, probe.rolled)
	require.False(t, probe.committed)
	probe.mu.Unlock()
}
func (t *probeTx) Commit() error {
	t.probe.mu.Lock()
	defer t.probe.mu.Unlock()
	t.probe.committed = true
	return t.probe.commitErr
}
func (t *probeTx) Rollback() error {
	t.probe.mu.Lock()
	t.probe.rolled = true
	if t.probe.rolledCh != nil {
		close(t.probe.rolledCh)
		t.probe.rolledCh = nil
	}
	t.probe.mu.Unlock()
	return nil
}

func openProbe(t *testing.T, probe *transactionProbe) *sql.DB {
	t.Helper()
	name := "catalog_probe_" + t.Name()
	sql.Register(name, probeDriver{probe: probe})
	db, err := sql.Open(name, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestReadTransactionOptionsAndQueryFailure(t *testing.T) {
	for _, tc := range []struct {
		name          string
		profile       engineprofile.Profile
		wantIsolation sql.IsolationLevel
	}{
		{"sqlite", sqliteProfile(t), sql.LevelDefault},
		{"postgresql", mustProfile(t, "postgresql-17", engineprofile.Version{Known: true, Major: 17, Minor: 6}), sql.LevelRepeatableRead},
		{"mysql", mustProfile(t, "mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4}), sql.LevelRepeatableRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &transactionProbe{queryErr: errors.New("inspect query failed")}
			db := openProbe(t, probe)
			_, err := catalogread.Read(t.Context(), db, tc.profile, catalogread.Scope{})
			require.Error(t, err)
			probe.mu.Lock()
			require.Equal(t, tc.wantIsolation, probe.opts.Isolation)
			require.True(t, probe.opts.ReadOnly)
			require.True(t, probe.rolled)
			require.False(t, probe.committed)
			probe.mu.Unlock()
		})
	}
}

func mustProfile(t *testing.T, id string, version engineprofile.Version) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin(id, version)
	require.NoError(t, err)
	return p
}

var _ driver.ConnBeginTx = (*probeConn)(nil)
var _ driver.QueryerContext = (*probeConn)(nil)
var _ driver.Tx = (*probeTx)(nil)
