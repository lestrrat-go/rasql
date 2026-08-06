package rasql

import (
	"context"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

// SelectFrom starts a typed fluent SELECT builder for table.
// It selects every table column by default so All and One can decode T.
func SelectFrom[T any](client Client, table Table[T]) TypedSelectBuilder[T] {
	if isNilTable(table) {
		return TypedSelectBuilder[T]{builder: client.SelectFrom(query.Table{}).withError(fmt.Errorf("rasql: table must not be nil"))}
	}
	reference := table.QueryTable()
	definition := reference.Definition()
	columns := make([]string, len(definition.Columns))
	for index, column := range definition.Columns {
		columns[index] = column.Name
	}
	return TypedSelectBuilder[T]{builder: client.SelectFrom(reference).Select(columns...)}
}

// DecodeFrom starts a typed fluent SELECT builder for a custom result shape.
// R is explicit and T is inferred from table. R is mapped by its DecodeRow
// method when it has one, and projected column names map to R's rasql tags or
// snake-cased exported field names otherwise.
func DecodeFrom[R any, T any](client Client, table Table[T]) TypedSelectBuilder[R] {
	if isNilTable(table) {
		return TypedSelectBuilder[R]{builder: client.SelectFrom(query.Table{}).withError(fmt.Errorf("rasql: table must not be nil"))}
	}
	return TypedSelectBuilder[R]{builder: client.SelectFrom(table.QueryTable())}
}

// DecodeQueryFrom starts a typed fluent SELECT builder for a table with no Go row type.
// R is mapped by its DecodeRow method when it has one, and projected column names
// map to R's rasql tags or snake-cased exported field names otherwise.
func DecodeQueryFrom[R any](client Client, table query.Table) TypedSelectBuilder[R] {
	return TypedSelectBuilder[R]{builder: client.SelectFrom(table)}
}

// InnerJoin returns an INNER JOIN on table with on as its condition.
// It adapts a typed table for the dialect-neutral query API, which cannot
// import this package.
func InnerJoin[T any](table Table[T], on query.Expression) query.Join {
	if isNilTable(table) {
		return query.InnerJoin(query.Table{}, on)
	}
	return query.InnerJoin(table.QueryTable(), on)
}

// LeftJoin returns a LEFT JOIN on table with on as its condition.
// It adapts a typed table for the dialect-neutral query API, which cannot
// import this package.
func LeftJoin[T any](table Table[T], on query.Expression) query.Join {
	if isNilTable(table) {
		return query.LeftJoin(query.Table{}, on)
	}
	return query.LeftJoin(table.QueryTable(), on)
}

// TypedSelectBuilder builds a SELECT that decodes rows as T.
type TypedSelectBuilder[T any] struct {
	builder SelectBuilder
}

// Project adds projections created through the basic query API.
func (b TypedSelectBuilder[T]) Project(projections ...query.Projection) TypedSelectBuilder[T] {
	b.builder = b.builder.Project(projections...)
	return b
}

// Join adds joins created through the basic query API.
func (b TypedSelectBuilder[T]) Join(joins ...query.Join) TypedSelectBuilder[T] {
	b.builder = b.builder.Join(joins...)
	return b
}

// Where adds a predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made. Use one call
// with query.Or for a top-level OR.
func (b TypedSelectBuilder[T]) Where(expression query.Expression) TypedSelectBuilder[T] {
	b.builder = b.builder.Where(expression)
	return b
}

// WhereEqual adds an equality predicate for column and binds value.
// Repeated calls combine with AND in the order they were made, including calls to Where.
// Build and Query reject a column whose table is not part of the statement.
func (b TypedSelectBuilder[T]) WhereEqual(column query.Column, value any) TypedSelectBuilder[T] {
	b.builder = b.builder.Where(query.Equal(column, query.Bind(value)))
	return b
}

// WhereIn adds an IN predicate for column and binds each value.
// Repeated calls combine with AND in the order they were made, including calls to
// Where and WhereEqual. Build, Query, All, and One reject an empty value list,
// and reject a column whose table is not part of the statement.
func (b TypedSelectBuilder[T]) WhereIn(column query.Column, values ...any) TypedSelectBuilder[T] {
	if len(values) == 0 {
		b.builder = b.builder.withError(fmt.Errorf("rasql: IN requires at least one value"))
		return b
	}
	binds := make([]query.Expression, len(values))
	for i, value := range values {
		binds[i] = query.Bind(value)
	}
	b.builder = b.builder.Where(query.In(column, binds...))
	return b
}

// GroupBy adds grouping expressions created through the basic query API.
// Repeated calls append in the order they were made.
func (b TypedSelectBuilder[T]) GroupBy(expressions ...query.Expression) TypedSelectBuilder[T] {
	b.builder = b.builder.GroupBy(expressions...)
	return b
}

// Having adds a grouped predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made, exactly as Where
// does. Use one call with query.Or for a top-level OR.
func (b TypedSelectBuilder[T]) Having(expression query.Expression) TypedSelectBuilder[T] {
	b.builder = b.builder.Having(expression)
	return b
}

// Order adds ordering created through the basic query API.
func (b TypedSelectBuilder[T]) Order(orders ...query.Order) TypedSelectBuilder[T] {
	b.builder = b.builder.Order(orders...)
	return b
}

// OrderAsc adds ascending ordering for column.
func (b TypedSelectBuilder[T]) OrderAsc(column query.Column) TypedSelectBuilder[T] {
	b.builder = b.builder.Order(query.Asc(column))
	return b
}

// OrderDesc adds descending ordering for column.
func (b TypedSelectBuilder[T]) OrderDesc(column query.Column) TypedSelectBuilder[T] {
	b.builder = b.builder.Order(query.Desc(column))
	return b
}

// Limit sets the maximum number of result rows.
func (b TypedSelectBuilder[T]) Limit(limit int) TypedSelectBuilder[T] {
	b.builder = b.builder.Limit(limit)
	return b
}

// Offset sets the number of result rows to skip.
func (b TypedSelectBuilder[T]) Offset(offset int) TypedSelectBuilder[T] {
	b.builder = b.builder.Offset(offset)
	return b
}

// Build validates and renders the statement without executing it.
func (b TypedSelectBuilder[T]) Build() (render.Statement, error) {
	return b.builder.Build()
}

// Query returns a rangeable sequence that decodes each result row as T.
func (b TypedSelectBuilder[T]) Query(ctx context.Context) (iter.Seq2[T, error], error) {
	rows, err := b.builder.Query(ctx)
	if err != nil {
		return nil, err
	}
	return decodeRows[T](rows), nil
}

// All collects every row from Query.
func (b TypedSelectBuilder[T]) All(ctx context.Context) ([]T, error) {
	rows, err := b.Query(ctx)
	if err != nil {
		return nil, err
	}
	return collectAll(rows)
}

// Count executes COUNT(*) over the rows the statement matches.
// It ignores the projections that decode T, ignores ordering, and reports an
// error when the builder sets a limit or an offset: count an unpaged builder,
// then page a copy of it for the rows.
// Like [TypedSelectBuilder.One], it reports [ErrNoRows] or [ErrMultipleRows]
// when the database returns anything other than the one row COUNT(*) produces.
func (b TypedSelectBuilder[T]) Count(ctx context.Context) (int64, error) {
	return b.builder.Count(ctx)
}

// One returns exactly one row from Query.
// It returns [ErrNoRows] when the statement matched no rows and
// [ErrMultipleRows] when it matched more than one.
func (b TypedSelectBuilder[T]) One(ctx context.Context) (T, error) {
	var zero T
	rows, err := b.Query(ctx)
	if err != nil {
		return zero, err
	}
	return exactlyOne(rows)
}
