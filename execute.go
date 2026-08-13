package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

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
	return collectAll(rows, 0)
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
