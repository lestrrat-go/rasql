package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// Executor runs rendered statements for one SQL dialect.
// Client implements it over a database/sql handle and Tx implements it over a
// transaction, so a builder assembled once runs against either without being
// rebuilt. The builders take an Executor at their terminal call rather than at
// the head of the chain for exactly that reason.
type Executor interface {
	// Dialect returns the dialect statements are rendered for.
	Dialect() dialect.Dialect
	// QueryRendered executes a rendered statement and returns its result rows.
	// The caller owns the rows; row.Scan closes them.
	QueryRendered(context.Context, render.Statement) (*sql.Rows, error)
	// ExecRendered executes a rendered statement that returns no rows.
	ExecRendered(context.Context, render.Statement) (sql.Result, error)
}

// Tx executes statements inside one database transaction.
// The caller owns the transaction: every path out of the function that called
// DB.Begin must reach Commit or Rollback. A bare defer of Rollback right after
// Begin is the intended shape, because Rollback reports no error once the
// transaction is finished.
type Tx struct {
	client Client
	tx     *sql.Tx
}

// WithHooks returns a copy of t that runs hooks around statements executed in
// this transaction. It preserves Tx, so callers still commit or roll back the
// same explicit transaction.
func (t Tx) WithHooks(hooks ...Hook) (Tx, error) {
	client, err := t.client.WithHooks(hooks...)
	if err != nil {
		return Tx{}, err
	}
	t.client = client
	return t, nil
}

// Dialect returns the dialect this transaction renders SQL for.
func (t Tx) Dialect() dialect.Dialect {
	return t.client.Dialect()
}

// QueryRendered executes statement inside the transaction and returns its
// result rows. The caller owns the rows; row.Scan closes them.
func (t Tx) QueryRendered(ctx context.Context, statement render.Statement) (*sql.Rows, error) {
	return t.client.QueryRendered(ctx, statement)
}

// ExecRendered executes a rendered statement inside the transaction.
func (t Tx) ExecRendered(ctx context.Context, statement render.Statement) (sql.Result, error) {
	return t.client.ExecRendered(ctx, statement)
}

// Commit commits the transaction. Every later Commit or Rollback finds it
// finished: a later Commit reports that, and a later Rollback reports nothing.
func (t Tx) Commit() error {
	if t.tx == nil {
		return fmt.Errorf("rasql: invalid transaction")
	}
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("rasql: commit transaction: %w", err)
	}
	return nil
}

// Rollback rolls the transaction back, and reports no error when it is already
// finished, whether by a successful Commit, an earlier Rollback, or a rollback
// database/sql performed when the context was cancelled. That is what makes a
// bare defer of Rollback right after Begin correct rather than an error a
// caller learns to discard.
func (t Tx) Rollback() error {
	if t.tx == nil {
		return fmt.Errorf("rasql: invalid transaction")
	}
	if err := t.tx.Rollback(); err != nil {
		if errors.Is(err, sql.ErrTxDone) {
			return nil
		}
		return fmt.Errorf("rasql: roll back transaction: %w", err)
	}
	return nil
}

var (
	_ Executor = Client{}
	_ Executor = Tx{}
)

// Query renders statement for x's dialect and returns a rangeable sequence of
// its rows. It reports validation and rendering errors before iteration starts
// and yields an execution error instead of a row once iteration begins.
// The statement runs when the sequence is first ranged over, not when Query
// returns, so a sequence that is never ranged opens no cursor to leak; a
// sequence that is ranged closes the underlying rows when it ends.
func Query(ctx context.Context, x Executor, statement query.Select) (iter.Seq2[row.Dynamic, error], error) {
	if isNil(x) {
		return nil, fmt.Errorf("rasql: executor must not be nil")
	}
	rendered, err := render.Select(x.Dialect(), statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	return scanRendered(ctx, x, rendered), nil
}

// QueryRendered executes a precompiled SELECT and decodes each result row as
// T. Use it for static templates or other trusted SQL that the query builder
// does not model. The statement must already contain dialect-specific
// placeholders and bound arguments.
func QueryRendered[T any](ctx context.Context, x Executor, statement render.Statement) (iter.Seq2[T, error], error) {
	if err := validateRenderedQuery(x, statement); err != nil {
		return nil, err
	}
	return scanTypedRendered[T](ctx, x, statement), nil
}

// QueryRenderedAll executes a precompiled SELECT and collects every result as
// T. It returns an error when any row cannot be decoded.
func QueryRenderedAll[T any](ctx context.Context, x Executor, statement render.Statement) ([]T, error) {
	rows, err := QueryRendered[T](ctx, x, statement)
	if err != nil {
		return nil, err
	}
	return collectAll(rows)
}

// QueryRenderedOne executes a precompiled SELECT and decodes exactly one
// result as T. It returns [ErrNoRows] when no row is returned and
// [ErrMultipleRows] when more than one row is returned.
func QueryRenderedOne[T any](ctx context.Context, x Executor, statement render.Statement) (T, error) {
	var zero T
	rows, err := QueryRendered[T](ctx, x, statement)
	if err != nil {
		return zero, err
	}
	return exactlyOne(rows)
}

// QueryWrite renders a write statement and returns a rangeable sequence of the
// rows its RETURNING clause produces. The statement must carry at least one
// returning projection, and the dialect must support RETURNING, which MySQL
// does not.
// The statement runs through QueryContext, so a debug Handle that returns nil
// rows never executes it. Like Query, it runs the statement when the sequence
// is first ranged over rather than when QueryWrite returns, so a write whose
// sequence is abandoned never reaches the database.
func QueryWrite(ctx context.Context, x Executor, statement query.WriteStatement) (iter.Seq2[row.Dynamic, error], error) {
	rendered, err := renderQueryWrite(x, statement)
	if err != nil {
		return nil, err
	}
	return scanRendered(ctx, x, rendered), nil
}

func renderQueryWrite(x Executor, statement query.WriteStatement) (render.Statement, error) {
	if isNil(x) {
		return render.Statement{}, fmt.Errorf("rasql: executor must not be nil")
	}
	if isNil(statement) || len(statement.Returning()) == 0 {
		return render.Statement{}, fmt.Errorf("rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
	}
	rendered, err := render.Write(x.Dialect(), statement)
	if err != nil {
		return render.Statement{}, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return rendered, nil
}

func validateRenderedQuery(x Executor, statement render.Statement) error {
	if isNil(x) || isNil(x.Dialect()) {
		return fmt.Errorf("rasql: executor must not be nil")
	}
	if statement.SQL() == "" {
		return fmt.Errorf("rasql: statement SQL must not be empty")
	}
	return nil
}

// scanRendered defers running statement until the returned sequence is ranged
// over, so obtaining a sequence and abandoning it opens no cursor. Every
// terminal that hands result rows to row.Scan goes through it, which keeps the
// rule in one place: the *sql.Rows is created and consumed inside the same
// closure.
func scanRendered(ctx context.Context, x Executor, statement render.Statement) iter.Seq2[row.Dynamic, error] {
	return func(yield func(row.Dynamic, error) bool) {
		rows, err := x.QueryRendered(ctx, statement)
		if err != nil {
			yield(row.Dynamic{}, err)
			return
		}
		row.Scan(rows)(yield)
	}
}

// Exec renders and executes a write statement.
// It rejects a statement carrying a RETURNING clause, because ExecContext
// discards result rows; QueryWrite reads them instead.
func Exec(ctx context.Context, x Executor, statement query.WriteStatement) (sql.Result, error) {
	if isNil(x) {
		return nil, fmt.Errorf("rasql: executor must not be nil")
	}
	if !isNil(statement) && len(statement.Returning()) > 0 {
		return nil, fmt.Errorf("rasql: write statement has a RETURNING clause: use QueryWrite to read its rows")
	}
	rendered, err := render.Write(x.Dialect(), statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return x.ExecRendered(ctx, rendered)
}
