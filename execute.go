package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// Query renders statement for db's dialect and returns a rangeable sequence of
// its rows. It reports validation and rendering errors before iteration starts
// and yields an execution error instead of a row once iteration begins.
// The statement runs when the sequence is first ranged over, not when Query
// returns, so a sequence that is never ranged opens no cursor to leak; a
// sequence that is ranged closes the underlying rows when it ends.
func Query(ctx context.Context, db DB, statement query.Select) (iter.Seq2[row.Dynamic, error], error) {
	if err := db.valid(); err != nil {
		return nil, err
	}
	rendered, err := render.Select(db.Dialect(), statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	return scanRendered(ctx, db, rendered), nil
}

// QueryRendered executes a precompiled SELECT and decodes each result row as
// T. Use it for static templates or other trusted SQL that the query builder
// does not model. The statement must already contain dialect-specific
// placeholders and bound arguments.
func QueryRendered[T any](ctx context.Context, db DB, statement render.Statement) (iter.Seq2[T, error], error) {
	if err := db.validStatement(statement); err != nil {
		return nil, err
	}
	return scanTypedRendered[T](ctx, db, statement), nil
}

// QueryRenderedAll executes a precompiled SELECT and collects every result as
// T. It returns an error when any row cannot be decoded.
func QueryRenderedAll[T any](ctx context.Context, db DB, statement render.Statement) ([]T, error) {
	rows, err := QueryRendered[T](ctx, db, statement)
	if err != nil {
		return nil, err
	}
	return collectAll(rows)
}

// QueryRenderedOne executes a precompiled SELECT and decodes exactly one
// result as T. It returns [ErrNoRows] when no row is returned and
// [ErrMultipleRows] when more than one row is returned.
func QueryRenderedOne[T any](ctx context.Context, db DB, statement render.Statement) (T, error) {
	var zero T
	rows, err := QueryRendered[T](ctx, db, statement)
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
func QueryWrite(ctx context.Context, db DB, statement query.WriteStatement) (iter.Seq2[row.Dynamic, error], error) {
	rendered, err := renderQueryWrite(db, statement)
	if err != nil {
		return nil, err
	}
	return scanRendered(ctx, db, rendered), nil
}

// Exec renders and executes a write statement.
// It rejects a statement carrying a RETURNING clause, because ExecContext
// discards result rows; QueryWrite reads them instead.
func Exec(ctx context.Context, db DB, statement query.WriteStatement) (sql.Result, error) {
	if err := db.valid(); err != nil {
		return nil, err
	}
	if !isNil(statement) && len(statement.Returning()) > 0 {
		return nil, fmt.Errorf("rasql: write statement has a RETURNING clause: use QueryWrite to read its rows")
	}
	rendered, err := render.Write(db.Dialect(), statement)
	if err != nil {
		return nil, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return db.ExecRendered(ctx, rendered)
}

func renderQueryWrite(db DB, statement query.WriteStatement) (render.Statement, error) {
	if err := db.valid(); err != nil {
		return render.Statement{}, err
	}
	if isNil(statement) || len(statement.Returning()) == 0 {
		return render.Statement{}, fmt.Errorf("rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
	}
	rendered, err := render.Write(db.Dialect(), statement)
	if err != nil {
		return render.Statement{}, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return rendered, nil
}

// scanRendered defers running statement until the returned sequence is ranged
// over, so obtaining a sequence and abandoning it opens no cursor. Every
// terminal that hands result rows to row.Scan goes through it, which keeps the
// rule in one place: the *sql.Rows is created and consumed inside the same
// closure.
func scanRendered(ctx context.Context, db DB, statement render.Statement) iter.Seq2[row.Dynamic, error] {
	return func(yield func(row.Dynamic, error) bool) {
		rows, err := db.QueryRendered(ctx, statement)
		if err != nil {
			yield(row.Dynamic{}, err)
			return
		}
		row.Scan(rows)(yield)
	}
}
