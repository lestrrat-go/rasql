package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
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
		return Client{}, fmt.Errorf("rasql: queryer must not be nil")
	}
	if isNil(d) {
		return Client{}, fmt.Errorf("rasql: dialect must not be nil")
	}
	client := Client{queryer: queryer, dialect: d}
	if execer, ok := queryer.(Execer); ok {
		client.execer = execer
	}
	return client, nil
}

// Dialect returns the dialect this client renders SQL for.
// It returns nil for a zero Client.
func (c Client) Dialect() dialect.Dialect {
	return c.dialect
}

// Query renders statement and returns a rangeable sequence of rows.
// It reports validation and rendering errors before iteration starts.
func (c Client) Query(ctx context.Context, statement query.Select) (iter.Seq2[row.Row, error], error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	rendered, err := render.Select(c.dialect, statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	return c.QueryRendered(ctx, rendered)
}

// QueryRendered returns a rangeable sequence of rows from statement.
// It reports validation errors before iteration starts and yields execution or
// scanning errors instead of rows while it is ranged over.
func (c Client) QueryRendered(ctx context.Context, statement render.Statement) (iter.Seq2[row.Row, error], error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	if statement.SQL() == "" {
		return nil, fmt.Errorf("rasql: statement SQL must not be empty")
	}
	return func(yield func(row.Row, error) bool) {
		rows, err := c.queryer.QueryContext(ctx, statement.SQL(), statement.Args()...)
		if err != nil {
			yield(row.Row{}, fmt.Errorf("rasql: execute query: %w", err))
			return
		}
		if rows == nil {
			return
		}
		defer rows.Close()

		names, err := rows.Columns()
		if err != nil {
			yield(row.Row{}, fmt.Errorf("rasql: read result columns: %w", err))
			return
		}
		for rows.Next() {
			values := make([]any, len(names))
			destinations := make([]any, len(values))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				yield(row.Row{}, fmt.Errorf("rasql: scan result row: %w", err))
				return
			}
			decoded, err := row.New(names, values)
			if err != nil {
				yield(row.Row{}, fmt.Errorf("rasql: create result row: %w", err))
				return
			}
			if !yield(decoded, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(row.Row{}, fmt.Errorf("rasql: iterate result rows: %w", err))
		}
	}, nil
}

// QueryWrite renders a write statement and returns a rangeable sequence of the
// rows its RETURNING clause produces. The statement must carry at least one
// returning projection, and the dialect must support RETURNING, which MySQL
// does not.
// It reports validation and rendering errors before iteration starts and yields
// execution or scanning errors instead of rows while it is ranged over.
// The statement runs through QueryContext, so a debug Queryer that returns nil
// rows never executes it.
func (c Client) QueryWrite(ctx context.Context, statement query.WriteStatement) (iter.Seq2[row.Row, error], error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	if isNil(statement) || len(statement.Returning()) == 0 {
		return nil, fmt.Errorf("rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
	}
	rendered, err := render.Write(c.dialect, statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return c.QueryRendered(ctx, rendered)
}

// Exec renders and executes a write statement.
// It rejects a statement carrying a RETURNING clause, because ExecContext
// discards result rows; QueryWrite reads them instead.
func (c Client) Exec(ctx context.Context, statement query.WriteStatement) (sql.Result, error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	if isNil(c.execer) {
		return nil, fmt.Errorf("rasql: queryer does not support ExecContext")
	}
	if !isNil(statement) && len(statement.Returning()) > 0 {
		return nil, fmt.Errorf("rasql: write statement has a RETURNING clause: use QueryWrite to read its rows")
	}
	rendered, err := render.Write(c.dialect, statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return c.ExecRendered(ctx, rendered)
}

// ExecRendered executes a pre-rendered parameterized statement.
func (c Client) ExecRendered(ctx context.Context, statement render.Statement) (sql.Result, error) {
	if isNil(c.queryer) || isNil(c.dialect) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	if isNil(c.execer) {
		return nil, fmt.Errorf("rasql: queryer does not support ExecContext")
	}
	if statement.SQL() == "" {
		return nil, fmt.Errorf("rasql: statement SQL must not be empty")
	}
	result, err := c.execer.ExecContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return nil, fmt.Errorf("rasql: execute statement: %w", err)
	}
	return result, nil
}

func createTable(ctx context.Context, client Client, table schema.Table) error {
	if isNil(client.queryer) || isNil(client.dialect) {
		return fmt.Errorf("rasql: invalid client")
	}
	statement, err := render.CreateTable(client.dialect, table)
	if err != nil {
		return fmt.Errorf("rasql: render CREATE TABLE: %w", err)
	}
	if _, err := client.ExecRendered(ctx, statement); err != nil {
		return fmt.Errorf("rasql: execute CREATE TABLE: %w", err)
	}
	indexes, err := render.CreateIndexes(client.dialect, table)
	if err != nil {
		return fmt.Errorf("rasql: render CREATE INDEX: %w", err)
	}
	for _, index := range indexes {
		if _, err := client.ExecRendered(ctx, index); err != nil {
			return fmt.Errorf("rasql: execute CREATE INDEX: %w", err)
		}
	}
	return nil
}

// Create renders and executes table's definition followed by its indexes.
// Callers that require atomic DDL should construct the Client with a *sql.Tx.
func Create[T any](ctx context.Context, client Client, table Table[T]) error {
	if isNilTable(table) {
		return fmt.Errorf("rasql: table must not be nil")
	}
	return createTable(ctx, client, table.QueryTable().Definition())
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
