package rasql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

// DeleteBuilder builds and executes a DELETE statement through an immutable fluent API.
// Build and Exec reject a builder that carries no predicate, so a dropped Where cannot
// become a full-table delete. AllowAll states the full-table delete when that is the intent.
type DeleteBuilder struct {
	client   Client
	from     query.Table
	where    query.Expression
	hasWhere bool
	allowAll bool
	err      error
}

// DeleteFrom starts a fluent DELETE builder for table.
// Build and Exec report an error when the builder carries no predicate.
// Call AllowAll to delete every row of the target table.
func DeleteFrom[T any](client Client, table Table[T]) DeleteBuilder {
	if isNilTable(table) {
		return client.DeleteFrom(query.Table{}).withError(fmt.Errorf("rasql: table must not be nil"))
	}
	return client.DeleteFrom(table.QueryTable())
}

// DeleteFrom starts a fluent DELETE builder using table as its target.
// Build and Exec report an error when the builder carries no predicate.
// Call AllowAll to delete every row of the target table.
func (c Client) DeleteFrom(table query.Table) DeleteBuilder {
	return DeleteBuilder{client: c, from: table}
}

// Where sets the predicate using an expression created through the basic query API.
// It replaces any predicate set before it.
func (b DeleteBuilder) Where(expression query.Expression) DeleteBuilder {
	if b.err != nil {
		return b
	}
	if expression == nil {
		return b.withError(fmt.Errorf("rasql: WHERE expression must not be nil"))
	}
	b.where = expression
	b.hasWhere = true
	return b
}

// WhereEqual sets an equality predicate for column and binds value.
// It replaces any predicate set before it. Build and Exec reject a column whose
// table is not the delete target.
func (b DeleteBuilder) WhereEqual(column query.Column, value any) DeleteBuilder {
	if b.err != nil {
		return b
	}
	return b.Where(query.Equal(column, query.Bind(value)))
}

// AllowAll states that the statement is meant to delete every row of the target table,
// which Build and Exec otherwise reject. It sets no predicate and changes no rendered SQL.
// Build and Exec reject a builder that combines it with Where or WhereEqual, because the
// two state different intents.
func (b DeleteBuilder) AllowAll() DeleteBuilder {
	if b.err != nil {
		return b
	}
	b.allowAll = true
	return b
}

// Build validates and renders the statement without executing it.
func (b DeleteBuilder) Build() (render.Statement, error) {
	statement, err := b.statement()
	if err != nil {
		return render.Statement{}, err
	}
	return render.Delete(b.client.dialect, statement)
}

// Exec builds and executes the statement.
func (b DeleteBuilder) Exec(ctx context.Context) (sql.Result, error) {
	statement, err := b.statement()
	if err != nil {
		return nil, err
	}
	return b.client.Exec(ctx, statement)
}

func (b DeleteBuilder) statement() (query.Delete, error) {
	if b.err != nil {
		return query.Delete{}, b.err
	}
	if b.hasWhere && b.allowAll {
		return query.Delete{}, fmt.Errorf("rasql: AllowAll must not be combined with a WHERE predicate")
	}
	if !b.hasWhere && !b.allowAll {
		return query.Delete{}, fmt.Errorf("rasql: DELETE requires a WHERE predicate or an explicit AllowAll")
	}
	statement, err := query.NewDelete(b.from)
	if err != nil {
		return query.Delete{}, fmt.Errorf("rasql: build DELETE: %w", err)
	}
	if !b.hasWhere {
		return statement, nil
	}
	statement, err = statement.WithWhere(b.where)
	if err != nil {
		return query.Delete{}, fmt.Errorf("rasql: build DELETE: %w", err)
	}
	return statement, nil
}

func (b DeleteBuilder) withError(err error) DeleteBuilder {
	if b.err == nil {
		b.err = err
	}
	return b
}
