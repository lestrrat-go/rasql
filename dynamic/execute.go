package dynamic

import (
	"context"
	"fmt"
	"iter"
	"reflect"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/statement"
)

// Query renders stmt for db's dialect and returns a rangeable sequence of
// its rows. It reports validation and rendering errors before iteration starts
// and yields an execution error instead of a row once iteration begins.
// The statement runs when the sequence is first ranged over, not when Query
// returns, so a sequence that is never ranged opens no cursor to leak; a
// sequence that is ranged closes the underlying rows when it ends.
func Query(ctx context.Context, db rasql.DB, stmt query.Select) (iter.Seq2[Row, error], error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	rendered, err := render.Select(db.Dialect(), stmt)
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	return scanRendered(ctx, db, rendered), nil
}

// QueryWrite renders a write statement and returns a rangeable sequence of the
// rows its RETURNING clause produces. The statement must carry at least one
// returning projection, and the dialect must support RETURNING, which MySQL
// does not.
// The statement runs through QueryContext, so a debug Handle that returns nil
// rows never executes it. Like Query, it runs the statement when the sequence
// is first ranged over rather than when QueryWrite returns, so a write whose
// sequence is abandoned never reaches the database.
func QueryWrite(ctx context.Context, db rasql.DB, stmt query.WriteStatement) (iter.Seq2[Row, error], error) {
	rendered, err := renderQueryWrite(db, stmt)
	if err != nil {
		return nil, err
	}
	return scanRendered(ctx, db, rendered), nil
}

func renderQueryWrite(db rasql.DB, stmt query.WriteStatement) (statement.Statement, error) {
	if err := db.Validate(); err != nil {
		return statement.Statement{}, err
	}
	if isNil(stmt) || len(stmt.Returning()) == 0 {
		return statement.Statement{}, fmt.Errorf("rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
	}
	rendered, err := render.Write(db.Dialect(), stmt)
	if err != nil {
		return statement.Statement{}, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return rendered, nil
}

// scanRendered defers running stmt until the returned sequence is ranged
// over, so obtaining a sequence and abandoning it opens no cursor. Every
// terminal that hands result rows to Scan goes through it, which keeps the
// rule in one place: the *sql.Rows is created and consumed inside the same
// closure.
func scanRendered(ctx context.Context, db rasql.DB, stmt statement.Statement) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		rows, err := db.QueryRendered(ctx, stmt)
		if err != nil {
			yield(Row{}, err)
			return
		}
		Scan(rows)(yield)
	}
}

// exactlyOne requires that rows yields exactly one value. It returns
// [rasql.ErrNoRows] for an empty sequence and [rasql.ErrMultipleRows] as soon
// as a second value arrives, so every caller that expects one row reports the
// same sentinels root's own single-row terminals do.
func exactlyOne[T any](rows iter.Seq2[T, error]) (T, error) {
	var zero T
	var result T
	count := 0
	for value, err := range rows {
		if err != nil {
			return zero, err
		}
		result = value
		count++
		if count > 1 {
			return zero, rasql.ErrMultipleRows
		}
	}
	if count != 1 {
		return zero, rasql.ErrNoRows
	}
	return result, nil
}

// isNil is a verbatim copy of root's reflect-based nil check. Ten lines of
// duplication is the accepted cost of the one-way import rule: dynamic
// imports rasql for DB, so rasql can never import dynamic, and root keeps its
// own unexported copy rather than exporting one for this package alone.
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
