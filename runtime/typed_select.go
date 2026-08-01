package runtime

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// Table associates a reusable table reference with the Go type for one of its rows.
type Table[T any] struct {
	reference query.TableRef
}

// NewTable creates a typed table from a validated reusable table reference.
func NewTable[T any](reference query.TableRef) (Table[T], error) {
	if err := reference.Table().Validate(); err != nil {
		return Table[T]{}, fmt.Errorf("runtime: table reference: %w", err)
	}
	return Table[T]{reference: reference}, nil
}

// MustTable creates a typed table or panics when reference is invalid.
// It is intended for generated or otherwise static schema descriptors.
func MustTable[T any](reference query.TableRef) Table[T] {
	table, err := NewTable[T](reference)
	if err != nil {
		panic(err)
	}
	return table
}

// Ref returns the underlying reusable table reference.
func (t Table[T]) Ref() query.TableRef {
	return t.reference
}

// SelectFrom starts a typed fluent SELECT builder for table.
// It selects every table column by default so All and One can decode T.
func SelectFrom[T any](client Client, table Table[T]) TypedSelectBuilder[T] {
	reference := table.Ref()
	definition := reference.Table()
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

// All builds, executes, and decodes every result row as T.
func (b TypedSelectBuilder[T]) All(ctx context.Context) ([]T, error) {
	rows, err := b.builder.Query(ctx)
	if err != nil {
		return nil, err
	}
	decoded := make([]T, len(rows))
	for index, result := range rows {
		value, err := row.Decode[T](result)
		if err != nil {
			return nil, fmt.Errorf("runtime: decode row %d: %w", index, err)
		}
		decoded[index] = value
	}
	return decoded, nil
}

// One builds, executes, and decodes exactly one result row as T.
func (b TypedSelectBuilder[T]) One(ctx context.Context) (T, error) {
	var zero T
	rows, err := b.builder.Query(ctx)
	if err != nil {
		return zero, err
	}
	if len(rows) != 1 {
		return zero, fmt.Errorf("runtime: expected one row, got %d", len(rows))
	}
	decoded, err := row.Decode[T](rows[0])
	if err != nil {
		return zero, fmt.Errorf("runtime: decode row: %w", err)
	}
	return decoded, nil
}
