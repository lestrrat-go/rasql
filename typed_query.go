package rasql

import (
	"context"
	"iter"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
)

// TypedSelectFrom starts the compile-time checked query facade for table.
func TypedSelectFrom[T any](table Table[T]) SafeSelectBuilder[T] {
	return SafeSelectBuilder[T]{inner: SelectFrom(table)}
}

// SafeSelectBuilder accepts only typed predicates while retaining the same
// immutable execution and rendering behavior as TypedSelectBuilder.
type SafeSelectBuilder[T any] struct{ inner TypedSelectBuilder[T] }

func (b SafeSelectBuilder[T]) Project(projections ...query.Projection) SafeSelectBuilder[T] {
	b.inner = b.inner.Project(projections...)
	return b
}

func (b SafeSelectBuilder[T]) Join(joins ...query.TypedJoin) SafeSelectBuilder[T] {
	converted := make([]query.Join, len(joins))
	for i, join := range joins {
		converted[i] = join.Join()
	}
	b.inner = b.inner.Join(converted...)
	return b
}

func (b SafeSelectBuilder[T]) Where(predicate query.Predicate) SafeSelectBuilder[T] {
	b.inner = b.inner.Where(predicate.Expression())
	return b
}

func (b SafeSelectBuilder[T]) GroupBy(expressions ...query.Expression) SafeSelectBuilder[T] {
	b.inner = b.inner.GroupBy(expressions...)
	return b
}

func (b SafeSelectBuilder[T]) Having(predicate query.Predicate) SafeSelectBuilder[T] {
	b.inner = b.inner.Having(predicate.Expression())
	return b
}

func (b SafeSelectBuilder[T]) Order(orders ...query.Order) SafeSelectBuilder[T] {
	b.inner = b.inner.Order(orders...)
	return b
}

func (b SafeSelectBuilder[T]) Distinct() SafeSelectBuilder[T] {
	b.inner = b.inner.Distinct()
	return b
}

func (b SafeSelectBuilder[T]) Limit(limit int) SafeSelectBuilder[T] {
	b.inner = b.inner.Limit(limit)
	return b
}

func (b SafeSelectBuilder[T]) Offset(offset int) SafeSelectBuilder[T] {
	b.inner = b.inner.Offset(offset)
	return b
}

func (b SafeSelectBuilder[T]) Build(d dialect.Dialect) (stmt.Statement, error) {
	return b.inner.Build(d)
}

func (b SafeSelectBuilder[T]) Select() (query.Select, error) { return b.inner.Select() }

func (b SafeSelectBuilder[T]) Result() (query.ResultQuery, error) { return b.inner.Result() }

func (b SafeSelectBuilder[T]) Query(ctx context.Context, db DB) (iter.Seq2[T, error], error) {
	return b.inner.Query(ctx, db)
}

func (b SafeSelectBuilder[T]) All(ctx context.Context, db DB) ([]T, error) {
	return b.inner.All(ctx, db)
}

func (b SafeSelectBuilder[T]) One(ctx context.Context, db DB) (T, error) {
	return b.inner.One(ctx, db)
}

func (b SafeSelectBuilder[T]) Count(ctx context.Context, db DB) (int64, error) {
	return b.inner.Count(ctx, db)
}

func (b SafeSelectBuilder[T]) CountPage(ctx context.Context, db DB) (int64, error) {
	return b.inner.CountPage(ctx, db)
}
