package rasql

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// SelectBuilder builds and executes a SELECT statement through an immutable fluent API.
type SelectBuilder struct {
	client  Client
	builder render.SelectBuilder
}

// SelectFrom starts a fluent SELECT builder using table as its primary table.
func (c Client) SelectFrom(table query.TableRef) SelectBuilder {
	return SelectBuilder{client: c, builder: render.SelectFrom(c.dialect, table)}
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

// Where sets the predicate using an expression created through the basic query API.
func (b SelectBuilder) Where(expression query.Expression) SelectBuilder {
	b.builder = b.builder.Where(expression)
	return b
}

// WhereEqual sets an equality predicate for a primary-table column and binds value.
func (b SelectBuilder) WhereEqual(columnName string, value any) SelectBuilder {
	b.builder = b.builder.WhereEqual(columnName, value)
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

// Build validates and renders the statement without executing it.
func (b SelectBuilder) Build() (render.Statement, error) {
	return b.builder.Build()
}

// Query builds the statement and returns its result rows.
func (b SelectBuilder) Query(ctx context.Context) ([]row.Row, error) {
	statement, err := b.Build()
	if err != nil {
		return nil, fmt.Errorf("rasql: render SELECT: %w", err)
	}
	return b.client.QueryRendered(ctx, statement)
}
