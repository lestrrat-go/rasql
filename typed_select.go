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
	reference := table.Ref()
	definition := reference.Definition()
	columns := make([]string, len(definition.Columns))
	for index, column := range definition.Columns {
		columns[index] = column.Name
	}
	return TypedSelectBuilder[T]{builder: client.SelectFrom(reference).Select(columns...)}
}

// TypedSelectBuilder builds a SELECT that decodes rows as T.
type TypedSelectBuilder[T any] struct {
	builder SelectBuilder
}

// Where sets the predicate using an expression created through the basic query API.
func (b TypedSelectBuilder[T]) Where(expression query.Expression) TypedSelectBuilder[T] {
	b.builder = b.builder.Where(expression)
	return b
}

// WhereEqual sets an equality predicate for a primary-table column and binds value.
func (b TypedSelectBuilder[T]) WhereEqual(columnName string, value any) TypedSelectBuilder[T] {
	b.builder = b.builder.WhereEqual(columnName, value)
	return b
}

// Order adds ordering created through the basic query API.
func (b TypedSelectBuilder[T]) Order(orders ...query.Order) TypedSelectBuilder[T] {
	b.builder = b.builder.Order(orders...)
	return b
}

// OrderAsc adds ascending ordering for a primary-table column.
func (b TypedSelectBuilder[T]) OrderAsc(columnName string) TypedSelectBuilder[T] {
	b.builder = b.builder.OrderAsc(columnName)
	return b
}

// OrderDesc adds descending ordering for a primary-table column.
func (b TypedSelectBuilder[T]) OrderDesc(columnName string) TypedSelectBuilder[T] {
	b.builder = b.builder.OrderDesc(columnName)
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
// It yields one final error instead of a value when execution or decoding fails.
func (b TypedSelectBuilder[T]) Query(ctx context.Context) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		index := 0
		for result, err := range b.builder.Query(ctx) {
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
	}
}

// All collects every row from Query.
func (b TypedSelectBuilder[T]) All(ctx context.Context) ([]T, error) {
	decoded := make([]T, 0)
	for value, err := range b.Query(ctx) {
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
	var result T
	count := 0
	for value, err := range b.Query(ctx) {
		if err != nil {
			return zero, err
		}
		result = value
		count++
	}
	if count != 1 {
		return zero, fmt.Errorf("rasql: expected one row, got %d", count)
	}
	return result, nil
}
