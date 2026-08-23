package dynamic

import (
	"context"
	"database/sql"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/stmt"
)

// DeleteBuilder builds a DELETE statement through an immutable fluent API and
// executes it against a DB at its terminal call.
// Build and Exec reject a builder that carries no predicate, so a dropped Where cannot
// become a full-table delete. AllowAll states the full-table delete when that is the intent.
type DeleteBuilder struct {
	from       query.TableRef
	predicates []query.Expression
	allowAll   bool
	err        error
}

// DeleteReturningBuilder adds a RETURNING clause to a delete builder.
// Query returns dynamic rows.
type DeleteReturningBuilder struct {
	builder     DeleteBuilder
	projections []query.Projection
	err         error
}

// DeleteFrom starts a fluent DELETE builder using table as its target.
// It is the counterpart of rasql.DeleteFrom for a query.TableRef with no Go
// row type.
// Build and Exec report an error when the builder carries no predicate.
// Call AllowAll to delete every row of the target table.
func DeleteFrom(table query.TableRef) DeleteBuilder {
	return DeleteBuilder{from: table}
}

// Where adds a predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made. Use one call
// with query.Or for a top-level OR.
func (b DeleteBuilder) Where(expression query.Expression) DeleteBuilder {
	if b.err != nil {
		return b
	}
	if isNil(expression) {
		return b.withError(fmt.Errorf("rasql: WHERE expression must not be nil"))
	}
	b = b.clone()
	b.predicates = append(b.predicates, expression)
	return b
}

// WhereEqual adds an equality predicate for column and binds value.
// Repeated calls combine with AND in the order they were made, including calls to Where.
// Build and Exec reject a column whose table is not the delete target.
func (b DeleteBuilder) WhereEqual(column query.ColumnRef, value any) DeleteBuilder {
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
func (b DeleteBuilder) WhereIn(column query.ColumnRef, values ...any) DeleteBuilder {
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
// because a predicate and a full-table delete state different intents.
func (b DeleteBuilder) AllowAll() DeleteBuilder {
	if b.err != nil {
		return b
	}
	b.allowAll = true
	return b
}

// Returning adds projections to the delete's RETURNING clause. Query runs the
// delete and returns dynamic rows.
func (b DeleteBuilder) Returning(projections ...query.Projection) DeleteReturningBuilder {
	return DeleteReturningBuilder{builder: b}.Returning(projections...)
}

// Build validates the statement and renders it for d without executing it.
func (b DeleteBuilder) Build(d dialect.Dialect) (stmt.Statement, error) {
	s, err := b.statement()
	if err != nil {
		return stmt.Statement{}, err
	}
	return render.Delete(d, s)
}

// Exec builds the statement and executes it.
func (b DeleteBuilder) Exec(ctx context.Context, db rasql.DB) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	s, err := b.statement()
	if err != nil {
		return nil, err
	}
	return rasql.Exec(ctx, db, s)
}

// Returning adds projections to an existing delete RETURNING builder.
func (b DeleteReturningBuilder) Returning(projections ...query.Projection) DeleteReturningBuilder {
	if b.err != nil {
		return b
	}
	if len(projections) == 0 {
		return b.withError(fmt.Errorf("rasql: RETURNING requires at least one projection"))
	}
	b = b.clone()
	b.projections = append(b.projections, projections...)
	return b
}

// Build validates and renders the delete with its RETURNING clause.
func (b DeleteReturningBuilder) Build(d dialect.Dialect) (stmt.Statement, error) {
	s, err := b.statement()
	if err != nil {
		return stmt.Statement{}, err
	}
	return render.Delete(d, s)
}

// Query runs the delete and returns its RETURNING rows as a rangeable sequence
// of dynamic rows. The statement runs when the sequence is first ranged.
func (b DeleteReturningBuilder) Query(ctx context.Context, db rasql.DB) (iter.Seq2[Row, error], error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	s, err := b.statement()
	if err != nil {
		return nil, err
	}
	return QueryWrite(ctx, db, s)
}

func (b DeleteReturningBuilder) statement() (query.Delete, error) {
	if b.err != nil {
		return query.Delete{}, b.err
	}
	s, err := b.builder.statement()
	if err != nil {
		return query.Delete{}, err
	}
	s, err = s.WithReturning(b.projections...)
	if err != nil {
		return query.Delete{}, fmt.Errorf("rasql: build DELETE RETURNING: %w", err)
	}
	return s, nil
}

func (b DeleteReturningBuilder) withError(err error) DeleteReturningBuilder {
	if b.err == nil {
		b.err = err
	}
	return b
}

func (b DeleteReturningBuilder) clone() DeleteReturningBuilder {
	copy := b
	copy.projections = append([]query.Projection(nil), b.projections...)
	return copy
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
	s, err := query.NewDelete(b.from)
	if err != nil {
		return query.Delete{}, fmt.Errorf("rasql: build DELETE: %w", err)
	}
	if !hasPredicate {
		s, err = s.AllowAll()
		if err != nil {
			return query.Delete{}, fmt.Errorf("rasql: build DELETE: %w", err)
		}
		return s, nil
	}
	s, err = s.WithWhere(predicate)
	if err != nil {
		return query.Delete{}, fmt.Errorf("rasql: build DELETE: %w", err)
	}
	return s, nil
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
