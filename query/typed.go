package query

// TypedColumn is a compile-time typed reference to a non-null column.
type TypedColumn[Row, Value any] struct{ ref ColumnRef }

// NullableColumn is a compile-time typed reference to a nullable column.
type NullableColumn[Row, Value any] struct{ ref ColumnRef }

func TypedColumnOf[Row, Value any](ref ColumnRef) TypedColumn[Row, Value] {
	return TypedColumn[Row, Value]{ref: ref}
}

func NullableColumnOf[Row, Value any](ref ColumnRef) NullableColumn[Row, Value] {
	return NullableColumn[Row, Value]{ref: ref}
}

func (c TypedColumn[Row, Value]) Ref() ColumnRef                     { return c.ref }
func (c NullableColumn[Row, Value]) Ref() ColumnRef                  { return c.ref }
func (c TypedColumn[Row, Value]) Name() string                       { return c.ref.Name() }
func (c NullableColumn[Row, Value]) Name() string                    { return c.ref.Name() }
func (c TypedColumn[Row, Value]) Source() RelationRef                { return c.ref.Source() }
func (c NullableColumn[Row, Value]) Source() RelationRef             { return c.ref.Source() }
func (c TypedColumn[Row, Value]) ProjectedExpression() Expression    { return c.ref }
func (c TypedColumn[Row, Value]) ResultAlias() string                { return "" }
func (c TypedColumn[Row, Value]) As(alias string) Projection         { return c.ref.As(alias) }
func (c NullableColumn[Row, Value]) ProjectedExpression() Expression { return c.ref }
func (c NullableColumn[Row, Value]) ResultAlias() string             { return "" }
func (c NullableColumn[Row, Value]) As(alias string) Projection      { return c.ref.As(alias) }

// Predicate is an opaque typed boolean expression accepted by SafeSelectBuilder.
type Predicate struct{ expression Expression }

func (p Predicate) Expression() Expression { return p.expression }

func typedPredicate(expression Expression) Predicate { return Predicate{expression: expression} }

func EqualValue[R, V any](column TypedColumn[R, V], value V) Predicate {
	return typedPredicate(Equal(column.ref, value))
}

func EqualNullableValue[R, V any](column NullableColumn[R, V], value V) Predicate {
	return typedPredicate(Equal(column.ref, value))
}

func EqualColumns[L, R, V any](left TypedColumn[L, V], right TypedColumn[R, V]) Predicate {
	return typedPredicate(Equal(left.ref, right.ref))
}

func LessValue[R, V any](column TypedColumn[R, V], value V) Predicate {
	return typedPredicate(LessThan(column.ref, value))
}

func LessOrEqualValue[R, V any](column TypedColumn[R, V], value V) Predicate {
	return typedPredicate(LessThanOrEqual(column.ref, value))
}

func GreaterValue[R, V any](column TypedColumn[R, V], value V) Predicate {
	return typedPredicate(GreaterThan(column.ref, value))
}

func GreaterOrEqualValue[R, V any](column TypedColumn[R, V], value V) Predicate {
	return typedPredicate(GreaterThanOrEqual(column.ref, value))
}

func InValues[R, V any](column TypedColumn[R, V], values ...V) Predicate {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return typedPredicate(In(column.ref, args...))
}

func AndPredicates(predicates ...Predicate) Predicate {
	expressions := make([]Expression, len(predicates))
	for i, predicate := range predicates {
		expressions[i] = predicate.Expression()
	}
	return typedPredicate(And(expressions...))
}

func OrPredicates(predicates ...Predicate) Predicate {
	expressions := make([]Expression, len(predicates))
	for i, predicate := range predicates {
		expressions[i] = predicate.Expression()
	}
	return typedPredicate(Or(expressions...))
}

func NotPredicate(predicate Predicate) Predicate {
	return typedPredicate(Negate(predicate.expression))
}

func TypedIsNull[R, V any](column NullableColumn[R, V]) Predicate {
	return typedPredicate(IsNull(column.ref))
}

func TypedIsNotNull[R, V any](column NullableColumn[R, V]) Predicate {
	return typedPredicate(IsNotNull(column.ref))
}

func AssignValue[R, V any](column TypedColumn[R, V], value V) Assignment {
	return Set(column.ref, value)
}

func AssignNullableValue[R, V any](column NullableColumn[R, V], value V) Assignment {
	return Set(column.ref, value)
}

// TypedJoin is a join whose condition has been checked by typed constructors.
type TypedJoin struct {
	join Join
}

func TypedInnerJoin(source RelationSource, on Predicate) TypedJoin {
	return TypedJoin{join: InnerJoin(source, on.Expression())}
}

func TypedLeftJoin(source RelationSource, on Predicate) TypedJoin {
	return TypedJoin{join: LeftJoin(source, on.Expression())}
}

func (j TypedJoin) Join() Join { return j.join }
