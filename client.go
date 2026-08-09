package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Queryer executes rendered SELECT statements. It is implemented by *sql.DB and *sql.Tx.
// A debug Queryer may return nil rows after logging a query; row.Scan treats that as no result rows.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Execer executes statements that do not return rows.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Handle is a database/sql handle that both reads rows and executes statements.
// *sql.DB and *sql.Tx implement it. New requires it, so a Client can always run
// a write; a queryer that only reads is rejected where it is supplied rather
// than where a write is attempted.
type Handle interface {
	Queryer
	Execer
}

// Client executes queries for a fixed SQL dialect.
// Its methods are safe for concurrent use when its Handle is safe for
// concurrent use.
type Client struct {
	handle  Handle
	dialect dialect.Dialect
	hooks   []Hook
}

// New creates a query client. It does not open a connection or start a
// transaction. Use Begin for a client bound to a transaction. Optional hooks
// observe statements executed by the returned client.
func New(handle Handle, d dialect.Dialect, hooks ...Hook) (Client, error) {
	if isNil(handle) {
		return Client{}, fmt.Errorf("rasql: handle must not be nil")
	}
	if isNil(d) {
		return Client{}, fmt.Errorf("rasql: dialect must not be nil")
	}
	client := Client{handle: handle, dialect: d}
	return client.WithHooks(hooks...)
}

// WithHooks returns a copy of c that runs hooks around rendered queries and
// mutations. Hooks are retained when the copy is used as an Executor. It does
// not wrap the database handle, so Client remains a Client and its SQL and
// bound arguments remain unchanged.
func (c Client) WithHooks(hooks ...Hook) (Client, error) {
	configured, err := appendHooks(c.hooks, hooks)
	if err != nil {
		return Client{}, err
	}
	c.hooks = configured
	return c, nil
}

// Dialect returns the dialect this client renders SQL for.
// It returns nil for a zero Client.
func (c Client) Dialect() dialect.Dialect {
	return c.dialect
}

// QueryRendered executes statement and returns its result rows.
// The caller owns the returned rows: hand them to row.Scan, which closes them,
// or close them directly. A debug Handle that logs the statement instead of
// running it may return nil rows, which row.Scan reads as no result rows.
func (c Client) QueryRendered(ctx context.Context, statement render.Statement) (*sql.Rows, error) {
	if isNil(c.handle) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	if statement.SQL() == "" {
		return nil, fmt.Errorf("rasql: statement SQL must not be empty")
	}
	operation := Operation{kind: QueryOperation, statement: statement}
	entered, err := c.beforeHooks(ctx, operation)
	if err != nil {
		return nil, afterHooks(ctx, operation, entered, err)
	}
	rows, err := c.handle.QueryContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		err = fmt.Errorf("rasql: execute query: %w", err)
	}
	if hookErr := afterHooks(ctx, operation, entered, err); hookErr != nil {
		if rows != nil {
			if closeErr := rows.Close(); closeErr != nil {
				hookErr = errors.Join(hookErr, fmt.Errorf("rasql: close query rows: %w", closeErr))
			}
		}
		return nil, hookErr
	}
	return rows, nil
}

// ExecRendered executes a pre-rendered parameterized statement.
func (c Client) ExecRendered(ctx context.Context, statement render.Statement) (sql.Result, error) {
	if isNil(c.handle) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	if statement.SQL() == "" {
		return nil, fmt.Errorf("rasql: statement SQL must not be empty")
	}
	operation := Operation{kind: ExecOperation, statement: statement}
	entered, err := c.beforeHooks(ctx, operation)
	if err != nil {
		return nil, afterHooks(ctx, operation, entered, err)
	}
	result, err := c.handle.ExecContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		err = fmt.Errorf("rasql: execute statement: %w", err)
	}
	if hookErr := afterHooks(ctx, operation, entered, err); hookErr != nil {
		return nil, hookErr
	}
	return result, nil
}

func createTable(ctx context.Context, x Executor, table schema.Table) error {
	if isNil(x) {
		return fmt.Errorf("rasql: executor must not be nil")
	}
	statement, err := render.CreateTable(x.Dialect(), table)
	if err != nil {
		return fmt.Errorf("rasql: render CREATE TABLE: %w", err)
	}
	if _, err := x.ExecRendered(ctx, statement); err != nil {
		return fmt.Errorf("rasql: execute CREATE TABLE: %w", err)
	}
	indexes, err := render.CreateIndexes(x.Dialect(), table)
	if err != nil {
		return fmt.Errorf("rasql: render CREATE INDEX: %w", err)
	}
	for _, index := range indexes {
		if _, err := x.ExecRendered(ctx, index); err != nil {
			return fmt.Errorf("rasql: execute CREATE INDEX: %w", err)
		}
	}
	return nil
}

// Create renders and executes table's definition followed by its indexes.
// Callers that require atomic DDL pass a Tx from Begin.
func Create[T any](ctx context.Context, x Executor, table Table[T]) error {
	if isNilTable(table) {
		return fmt.Errorf("rasql: table must not be nil")
	}
	return createTable(ctx, x, table.QueryTable().Definition())
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
