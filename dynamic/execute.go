package dynamic

import (
	"context"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/stmt"
)

// Query renders s for db's dialect and returns a rangeable sequence of
// its rows. It reports validation and rendering errors before iteration starts
// and yields an execution error instead of a row once iteration begins.
// The statement runs when the sequence is first ranged over, not when Query
// returns, so a sequence that is never ranged opens no cursor to leak; a
// sequence that is ranged closes the underlying rows when it ends.
func Query(ctx context.Context, db exec.DB, s query.Select) (iter.Seq2[Row, error], error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	rendered, err := render.Select(db.Dialect(), s)
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
func QueryWrite(ctx context.Context, db exec.DB, s query.WriteStatement) (iter.Seq2[Row, error], error) {
	rendered, err := exec.RenderWrite(db, s)
	if err != nil {
		return nil, err
	}
	return scanRendered(ctx, db, rendered), nil
}

// scanRendered defers running s until the returned sequence is ranged
// over, so obtaining a sequence and abandoning it opens no cursor. Every
// terminal that hands result rows to Scan goes through it, which keeps the
// rule in one place: the *sql.Rows is created and consumed inside the same
// closure.
func scanRendered(ctx context.Context, db exec.DB, s stmt.Statement) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		rows, err := db.QueryRendered(ctx, s)
		if err != nil {
			yield(Row{}, err)
			return
		}
		Scan(rows)(yield)
	}
}

// exactlyOne requires that rows yields exactly one value. It returns
// [exec.ErrNoRows] for an empty sequence and [exec.ErrMultipleRows] as soon
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
			return zero, exec.ErrMultipleRows
		}
	}
	if count != 1 {
		return zero, exec.ErrNoRows
	}
	return result, nil
}
