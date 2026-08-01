// Package runtime executes rendered queries through database/sql.
package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
)

// Queryer executes rendered SELECT statements. It is implemented by *sql.DB and *sql.Tx.
// A debug Queryer may return nil rows after logging a query; Client treats that as no result rows.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Execer executes statements that do not return rows.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Client executes queries for a fixed SQL dialect.
// Its methods are safe for concurrent use when its Queryer is safe for concurrent use.
type Client struct {
	queryer Queryer
	execer  Execer
	dialect dialect.Dialect
}

// New creates a query client. It does not open a connection or start a transaction.
func New(queryer Queryer, d dialect.Dialect) (Client, error) {
	if isNil(queryer) {
		return Client{}, fmt.Errorf("runtime: queryer must not be nil")
	}
	if isNil(d) {
		return Client{}, fmt.Errorf("runtime: dialect must not be nil")
	}
	client := Client{queryer: queryer, dialect: d}
	if execer, ok := queryer.(Execer); ok {
		client.execer = execer
	}
	return client, nil
}

// Query renders statement, executes it, and returns its result rows.
func (c Client) Query(ctx context.Context, statement query.Select) ([]row.Row, error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("runtime: invalid client")
	}
	rendered, err := render.Select(c.dialect, statement)
	if err != nil {
		return nil, fmt.Errorf("runtime: render SELECT: %w", err)
	}
	return c.QueryRendered(ctx, rendered)
}

// QueryRendered executes a pre-rendered parameterized statement.
func (c Client) QueryRendered(ctx context.Context, statement render.Statement) ([]row.Row, error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("runtime: invalid client")
	}
	if statement.SQL() == "" {
		return nil, fmt.Errorf("runtime: statement SQL must not be empty")
	}
	rows, err := c.queryer.QueryContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return nil, fmt.Errorf("runtime: execute query: %w", err)
	}
	if rows == nil {
		return nil, nil
	}
	return collect(rows)
}

// Exec renders and executes a write statement.
func (c Client) Exec(ctx context.Context, statement query.WriteStatement) (sql.Result, error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("runtime: invalid client")
	}
	if isNil(c.execer) {
		return nil, fmt.Errorf("runtime: queryer does not support ExecContext")
	}
	rendered, err := render.Write(c.dialect, statement)
	if err != nil {
		return nil, fmt.Errorf("runtime: render write statement: %w", err)
	}
	return c.ExecRendered(ctx, rendered)
}

// ExecRendered executes a pre-rendered parameterized statement.
func (c Client) ExecRendered(ctx context.Context, statement render.Statement) (sql.Result, error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("runtime: invalid client")
	}
	if isNil(c.execer) {
		return nil, fmt.Errorf("runtime: queryer does not support ExecContext")
	}
	if statement.SQL() == "" {
		return nil, fmt.Errorf("runtime: statement SQL must not be empty")
	}
	result, err := c.execer.ExecContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return nil, fmt.Errorf("runtime: execute statement: %w", err)
	}
	return result, nil
}

// CreateTable renders and executes a table definition followed by its indexes.
// Callers that require atomic DDL should construct the Client with a *sql.Tx.
func (c Client) CreateTable(ctx context.Context, table schema.Table) error {
	if isNil(c.queryer) || isNil(c.dialect) {
		return fmt.Errorf("runtime: invalid client")
	}
	statement, err := render.CreateTable(c.dialect, table)
	if err != nil {
		return fmt.Errorf("runtime: render CREATE TABLE: %w", err)
	}
	if _, err := c.ExecRendered(ctx, statement); err != nil {
		return fmt.Errorf("runtime: execute CREATE TABLE: %w", err)
	}
	indexes, err := render.CreateIndexes(c.dialect, table)
	if err != nil {
		return fmt.Errorf("runtime: render CREATE INDEX: %w", err)
	}
	for _, index := range indexes {
		if _, err := c.ExecRendered(ctx, index); err != nil {
			return fmt.Errorf("runtime: execute CREATE INDEX: %w", err)
		}
	}
	return nil
}

func collect(rows *sql.Rows) ([]row.Row, error) {
	defer rows.Close()

	names, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("runtime: read result columns: %w", err)
	}
	result := make([]row.Row, 0)
	for rows.Next() {
		values := make([]any, len(names))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("runtime: scan result row: %w", err)
		}
		decoded, err := row.New(names, values)
		if err != nil {
			return nil, fmt.Errorf("runtime: create result row: %w", err)
		}
		result = append(result, decoded)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runtime: iterate result rows: %w", err)
	}
	return result, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
