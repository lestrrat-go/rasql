package rasql

import (
	"context"
	"database/sql"
	"iter"

	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
)

// QueryRendered executes a precompiled SELECT and decodes each result row as
// T. Use it for static templates or other trusted SQL that the query builder
// does not model. The statement must already contain dialect-specific
// placeholders and bound arguments.
func QueryRendered[T any](ctx context.Context, db DB, s stmt.Statement) (iter.Seq2[T, error], error) {
	if err := db.ValidateStatement(s); err != nil {
		return nil, err
	}
	return scanTypedRendered[T](ctx, db, s), nil
}

// QueryRenderedAll executes a precompiled SELECT and collects every result as
// T. It returns an error when any row cannot be decoded.
func QueryRenderedAll[T any](ctx context.Context, db DB, s stmt.Statement) ([]T, error) {
	rows, err := QueryRendered[T](ctx, db, s)
	if err != nil {
		return nil, err
	}
	return collectAll(rows, 0)
}

// QueryRenderedOne executes a precompiled SELECT and decodes exactly one
// result as T. It returns [ErrNoRows] when no row is returned and
// [ErrMultipleRows] when more than one row is returned.
func QueryRenderedOne[T any](ctx context.Context, db DB, s stmt.Statement) (T, error) {
	var zero T
	rows, err := QueryRendered[T](ctx, db, s)
	if err != nil {
		return zero, err
	}
	return exactlyOne(rows)
}

// Exec renders and executes a write statement.
// It rejects a statement carrying a RETURNING clause, because ExecContext
// discards result rows; QueryWrite reads them instead.
func Exec(ctx context.Context, db DB, s query.WriteStatement) (sql.Result, error) {
	return exec.Write(ctx, db, s)
}
