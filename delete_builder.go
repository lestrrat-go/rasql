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
	client     Client
	from       query.Table
	predicates []query.Expression
	allowAll   bool
	err        error
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

// Where adds a predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made. Use one call
// with query.Or for a top-level OR.
func (b DeleteBuilder) Where(expression query.Expression) DeleteBuilder {
	if b.err != nil {
		return b
	}
	if expression == nil {
		return b.withError(fmt.Errorf("rasql: WHERE expression must not be nil"))
	}
	b = b.clone()
	b.predicates = append(b.predicates, expression)
	return b
}

// WhereEqual adds an equality predicate for column and binds value.
// Repeated calls combine with AND in the order they were made, including calls to Where.
// Build and Exec reject a column whose table is not the delete target.
func (b DeleteBuilder) WhereEqual(column query.Column, value any) DeleteBuilder {
	if b.err != nil {
		return b
	}
	return b.Where(query.Equal(column, query.Bind(value)))
}

// WhereIn adds an IN predicate for column and binds each value.
// Repeated calls combine with AND in the order they were made, including calls to
// Where and WhereEqual, and each call counts as the predicate that Build and Exec
// require. Build and Exec reject an empty value list, and reject a column whose
// table is not the delete target.
func (b DeleteBuilder) WhereIn(column query.Column, values ...any) DeleteBuilder {
	if b.err != nil {
		return b
	}
	if len(values) == 0 {
		return b.withError(fmt.Errorf("rasql: IN requires at least one value"))
	}
	binds := make([]query.Expression, len(values))
	for i, value := range values {
		binds[i] = query.Bind(value)
	}
	return b.Where(query.In(column, binds...))
}

// AllowAll states that the statement is meant to delete every row of the target table,
// which Build and Exec otherwise reject. It sets no predicate and changes no rendered SQL.
// Build and Exec reject a builder that combines it with Where, WhereEqual or WhereIn,
// because the two state different intents.
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
	// The guard reads the accumulated predicates, not a separate flag, so that
	// adding or dropping a Where cannot leave the check looking at stale state.
	predicate, hasPredicate := combinePredicates(b.predicates)
	if hasPredicate && b.allowAll {
		return query.Delete{}, fmt.Errorf("rasql: AllowAll must not be combined with a WHERE predicate")
	}
	if !hasPredicate && !b.allowAll {
		return query.Delete{}, fmt.Errorf("rasql: DELETE requires a WHERE predicate or an explicit AllowAll")
	}
	statement, err := query.NewDelete(b.from)
	if err != nil {
		return query.Delete{}, fmt.Errorf("rasql: build DELETE: %w", err)
	}
	if !hasPredicate {
		return statement, nil
	}
	statement, err = statement.WithWhere(predicate)
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

func (b DeleteBuilder) clone() DeleteBuilder {
	copy := b
	copy.predicates = append([]query.Expression(nil), b.predicates...)
	return copy
}

// combinePredicates reduces accumulated predicates to one expression.
// It reports false when there is no predicate to install.
func combinePredicates(predicates []query.Expression) (query.Expression, bool) {
	switch len(predicates) {
	case 0:
		return nil, false
	case 1:
		return predicates[0], true
	default:
		return query.And(predicates...), true
	}
}
