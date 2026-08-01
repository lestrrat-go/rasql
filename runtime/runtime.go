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
)

// Queryer is implemented by *sql.DB and *sql.Tx.
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
	rows, err := c.queryer.QueryContext(ctx, rendered.SQL(), rendered.Args()...)
	if err != nil {
		return nil, fmt.Errorf("runtime: execute SELECT: %w", err)
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
	result, err := c.execer.ExecContext(ctx, rendered.SQL(), rendered.Args()...)
	if err != nil {
		return nil, fmt.Errorf("runtime: execute write statement: %w", err)
	}
	return result, nil
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
