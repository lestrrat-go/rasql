package rasql

import (
	"context"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// SelectBuilder builds a SELECT statement through an immutable fluent API and
// executes it against an Executor at its terminal call. It carries no handle and
// no dialect, so one builder can be assembled once and run against a Client and
// a Tx alike.
type SelectBuilder struct {
	builder render.SelectBuilder
	err     error
}

// SelectQueryFrom starts a fluent SELECT builder using table as its primary
// table. It is the untyped counterpart of SelectFrom, for a query.Table with no
// Go row type, and its terminals yield row.Dynamic rather than a decoded type.
func SelectQueryFrom(table query.Table) SelectBuilder {
	return SelectBuilder{builder: render.SelectFrom(nil, table)}
}

// Select adds columns from the primary table by name.
func (b SelectBuilder) Select(columns ...string) SelectBuilder {
	b.builder = b.builder.Select(columns...)
	return b
}

// Project adds projections created through the basic query API.
func (b SelectBuilder) Project(projections ...query.Projection) SelectBuilder {
	b.builder = b.builder.Project(projections...)
	return b
}

// Join adds joins created through the basic query API.
func (b SelectBuilder) Join(joins ...query.Join) SelectBuilder {
	b.builder = b.builder.Join(joins...)
	return b
}

// Where adds a predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made. Use one call
// with query.Or for a top-level OR.
func (b SelectBuilder) Where(expression query.Expression) SelectBuilder {
	b.builder = b.builder.Where(expression)
	return b
}

// WhereEqual adds an equality predicate for a primary-table column and binds value.
// Repeated calls combine with AND in the order they were made, including calls to Where.
func (b SelectBuilder) WhereEqual(columnName string, value any) SelectBuilder {
	b.builder = b.builder.WhereEqual(columnName, value)
	return b
}

// WhereIn adds an IN predicate for a primary-table column and binds each value.
// Repeated calls combine with AND in the order they were made, including calls to
// Where and WhereEqual. Build and Query report an error when values is empty.
func (b SelectBuilder) WhereIn(columnName string, values ...any) SelectBuilder {
	b.builder = b.builder.WhereIn(columnName, values...)
	return b
}

// GroupBy adds grouping expressions created through the basic query API.
// Repeated calls append in the order they were made.
func (b SelectBuilder) GroupBy(expressions ...query.Expression) SelectBuilder {
	b.builder = b.builder.GroupBy(expressions...)
	return b
}

// GroupByColumns adds primary-table columns to the grouping by name.
// It is the untyped counterpart of passing a generated query.Column to GroupBy.
func (b SelectBuilder) GroupByColumns(names ...string) SelectBuilder {
	b.builder = b.builder.GroupByColumns(names...)
	return b
}

// Having adds a grouped predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made, exactly as Where
// does. Use one call with query.Or for a top-level OR.
func (b SelectBuilder) Having(expression query.Expression) SelectBuilder {
	b.builder = b.builder.Having(expression)
	return b
}

// Order adds ordering created through the basic query API.
func (b SelectBuilder) Order(orders ...query.Order) SelectBuilder {
	b.builder = b.builder.Order(orders...)
	return b
}

// OrderAsc adds ascending ordering for a primary-table column.
func (b SelectBuilder) OrderAsc(columnName string) SelectBuilder {
	b.builder = b.builder.OrderAsc(columnName)
	return b
}

// OrderDesc adds descending ordering for a primary-table column.
func (b SelectBuilder) OrderDesc(columnName string) SelectBuilder {
	b.builder = b.builder.OrderDesc(columnName)
	return b
}

// Distinct de-duplicates the result rows.
func (b SelectBuilder) Distinct() SelectBuilder {
	b.builder = b.builder.Distinct()
	return b
}

// Limit sets the maximum number of result rows.
func (b SelectBuilder) Limit(limit int) SelectBuilder {
	b.builder = b.builder.Limit(limit)
	return b
}

// Offset sets the number of result rows to skip.
func (b SelectBuilder) Offset(offset int) SelectBuilder {
	b.builder = b.builder.Offset(offset)
	return b
}

// Build validates the statement and renders it for d without executing it.
func (b SelectBuilder) Build(d dialect.Dialect) (render.Statement, error) {
	if b.err != nil {
		return render.Statement{}, b.err
	}
	return b.builder.WithDialect(d).Build()
}

// Query renders the statement for x's dialect and returns a rangeable sequence
// of rows. It reports validation and rendering errors before iteration starts
// and yields an execution error instead of a row once iteration begins.
// The statement runs when the sequence is first ranged over, not when Query
// returns, so a sequence that is never ranged opens no cursor to leak; a
// sequence that is ranged closes the underlying rows when it ends.
func (b SelectBuilder) Query(ctx context.Context, x Executor) (iter.Seq2[row.Dynamic, error], error) {
	if isNil(x) {
		return nil, fmt.Errorf("rasql: executor must not be nil")
	}
	statement, err := b.Build(x.Dialect())
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	return scanRendered(ctx, x, statement), nil
}

// Count executes COUNT(*) over the rows the statement matches.
// It ignores ordering and reports an error when the builder sets a limit or an
// offset: count an unpaged builder, then page a copy of it for the rows.
// A COUNT(*) statement returns exactly one row, so Count reports the same
// [ErrNoRows] and [ErrMultipleRows] as every other single-row read when the
// database returns anything else.
func (b SelectBuilder) Count(ctx context.Context, x Executor) (int64, error) {
	if isNil(x) {
		return 0, fmt.Errorf("rasql: executor must not be nil")
	}
	if b.err != nil {
		return 0, b.err
	}
	statement, err := b.builder.WithDialect(x.Dialect()).BuildCount()
	if err != nil {
		return 0, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	// Count consumes the sequence itself, so the statement runs before Count
	// returns either way. It goes through scanRendered so that no call site
	// outside that one closure holds a *sql.Rows.
	return exactlyOne(countValues(scanRendered(ctx, x, statement)))
}

// countValues adapts a sequence of result rows into the int64 held by each
// row's "count" result column, the name BuildCount projects COUNT(*) under.
func countValues(rows iter.Seq2[row.Dynamic, error]) iter.Seq2[int64, error] {
	return func(yield func(int64, error) bool) {
		for result, err := range rows {
			if err != nil {
				yield(0, err)
				return
			}
			count, err := row.Get[int64](result, "count")
			if err != nil {
				yield(0, err)
				return
			}
			if !yield(count, nil) {
				return
			}
		}
	}
}

func (b SelectBuilder) withError(err error) SelectBuilder {
	if b.err == nil {
		b.err = err
	}
	return b
}
