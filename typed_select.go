package rasql

import (
	"context"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// SelectFrom starts a typed fluent SELECT builder for table.
// It selects every table column by default so All and One can decode T.
func SelectFrom[T any](client Client, table Table[T]) TypedSelectBuilder[T] {
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
	return query.InnerJoin(table.QueryTable(), on)
}

// LeftJoin returns a LEFT JOIN on table with on as its condition.
// It adapts a typed table for the dialect-neutral query API, which cannot
// import this package.
func LeftJoin[T any](table Table[T], on query.Expression) query.Join {
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

// Where sets the predicate using an expression created through the basic query API.
func (b TypedSelectBuilder[T]) Where(expression query.Expression) TypedSelectBuilder[T] {
	b.builder = b.builder.Where(expression)
	return b
}

// WhereEqual sets an equality predicate for column and binds value.
// Build and Query reject a column whose table is not part of the statement.
func (b TypedSelectBuilder[T]) WhereEqual(column query.Column, value any) TypedSelectBuilder[T] {
	b.builder = b.builder.Where(query.Equal(column, query.Bind(value)))
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
	return func(yield func(T, error) bool) {
		var zero T
		index := 0
		for result, err := range rows {
			if err != nil {
				yield(zero, err)
				return
			}
			decoded, err := row.Decode[T](result)
			if err != nil {
				yield(zero, fmt.Errorf("rasql: decode row %d: %w", index, err))
				return
			}
			index++
			if !yield(decoded, nil) {
				return
			}
		}
	}, nil
}

// All collects every row from Query.
func (b TypedSelectBuilder[T]) All(ctx context.Context) ([]T, error) {
	rows, err := b.Query(ctx)
	if err != nil {
		return nil, err
	}
	decoded := make([]T, 0)
	for value, err := range rows {
		if err != nil {
			return nil, err
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

// One returns exactly one row from Query.
func (b TypedSelectBuilder[T]) One(ctx context.Context) (T, error) {
	var zero T
	rows, err := b.Query(ctx)
	if err != nil {
		return zero, err
	}
	var result T
	count := 0
	for value, err := range rows {
		if err != nil {
			return zero, err
		}
		result = value
		count++
		if count > 1 {
			return zero, fmt.Errorf("rasql: expected one row, got %d", count)
		}
	}
	if count != 1 {
		return zero, fmt.Errorf("rasql: expected one row, got %d", count)
	}
	return result, nil
}
