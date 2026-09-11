package rasql

import (
	"time"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/query"
)

// Ordered constrains the operators that need an ordering. time.Time appears
// as a plain union term rather than ~time.Time, because approximation is only
// legal for a type that is its own underlying type, and time.Time is a defined
// struct type. Ordering dates is common enough to be worth the exception.
type Ordered interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 | ~string | time.Time
}

// comparisonValue binds right with left's codec, exactly as EqualValue does.
// Threading the codec is the reason these operators exist as typed wrappers:
// a query.Expression built by hand carries no codec, so a column with a custom
// codec would bind its unencoded Go representation.
func comparisonValue[T any](left Expr[T], right T, build func(any, any) query.Binary) Predicate {
	id := bindplan.NextID()
	snapshot, copier, err := adoptBind(right, true)
	bind := query.Bind(bindToken{ID: id, Value: snapshot, Codec: left.codec, Copy: copier, Err: err})
	return Predicate{node: build(left.node, bind), source: left.source, bindErr: err}
}

func comparisonExpr[T any](left, right Expr[T], build func(any, any) query.Binary) Predicate {
	return Predicate{node: build(left.node, right.node), source: left.source, source2: right.source}
}

// Ordered comparisons. Each has an Expr form comparing two expressions and a
// Value form comparing an expression against a Go value.

func GreaterExpr[T Ordered](left, right Expr[T]) Predicate {
	return comparisonExpr(left, right, query.GreaterThan)
}
func GreaterValue[T Ordered](left Expr[T], right T) Predicate {
	return comparisonValue(left, right, query.GreaterThan)
}
func GreaterOrEqualExpr[T Ordered](left, right Expr[T]) Predicate {
	return comparisonExpr(left, right, query.GreaterThanOrEqual)
}
func GreaterOrEqualValue[T Ordered](left Expr[T], right T) Predicate {
	return comparisonValue(left, right, query.GreaterThanOrEqual)
}
func LessExpr[T Ordered](left, right Expr[T]) Predicate {
	return comparisonExpr(left, right, query.LessThan)
}
func LessValue[T Ordered](left Expr[T], right T) Predicate {
	return comparisonValue(left, right, query.LessThan)
}
func LessOrEqualExpr[T Ordered](left, right Expr[T]) Predicate {
	return comparisonExpr(left, right, query.LessThanOrEqual)
}
func LessOrEqualValue[T Ordered](left Expr[T], right T) Predicate {
	return comparisonValue(left, right, query.LessThanOrEqual)
}

// String operators. Constraining T to ~string is what stops LOWER(amount)
// and name LIKE 3 from compiling.

func LikeExpr[T ~string](left, pattern Expr[T]) Predicate {
	return comparisonExpr(left, pattern, query.Like)
}
func LikeValue[T ~string](left Expr[T], pattern T) Predicate {
	return comparisonValue(left, pattern, query.Like)
}
func LowerExpr[T ~string](value Expr[T]) Expr[T] {
	return Expr[T]{node: query.Lower(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}
func UpperExpr[T ~string](value Expr[T]) Expr[T] {
	return Expr[T]{node: query.Upper(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}
func LowerNullExpr[T ~string](value NullExpr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Lower(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}
func UpperNullExpr[T ~string](value NullExpr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Upper(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}

// Aggregates. Each returns NullExpr because SUM, MAX and AVG are NULL over an
// empty group even when their input column is NOT NULL. This follows MinExpr,
// which already made that choice.

func SumExpr[T Ordered](value Expr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Sum(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}
func SumNullExpr[T Ordered](value NullExpr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Sum(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}
func MaxExpr[T Ordered](value Expr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Max(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}
func MaxNullExpr[T Ordered](value NullExpr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Max(value.node), codec: value.codec, source: value.source, bindErr: value.bindErr}
}

// AvgExpr returns float64 rather than T, because the average of integers is
// not an integer. It carries no codec for the same reason: the result is a
// new value, not a decoded instance of the input column.
func AvgExpr[T Ordered](value Expr[T]) NullExpr[float64] {
	return NullExpr[float64]{node: query.Avg(value.node), source: value.source, bindErr: value.bindErr}
}
func AvgNullExpr[T Ordered](value NullExpr[T]) NullExpr[float64] {
	return NullExpr[float64]{node: query.Avg(value.node), source: value.source, bindErr: value.bindErr}
}

// InValues takes the first value separately so an empty IN list, which is not
// legal SQL, cannot be written at all.
func InValues[T comparable](left Expr[T], first T, rest ...T) Predicate {
	values := append([]T{first}, rest...)
	nodes := make([]any, len(values))
	var bindErr error
	for i, value := range values {
		id := bindplan.NextID()
		snapshot, copier, err := adoptBind(value, true)
		if err != nil && bindErr == nil {
			bindErr = err
		}
		nodes[i] = query.Bind(bindToken{ID: id, Value: snapshot, Codec: left.codec, Copy: copier, Err: err})
	}
	return Predicate{node: query.In(left.node, nodes...), source: left.source, bindErr: bindErr}
}

// InQuery reads the subquery's element type from its projection, so the
// subquery and the left-hand side are checked against each other at compile
// time. It reports an error rather than deferring one, which is what every
// other structural composition in this package does.
func InQuery[T comparable](left Expr[T], q Query[T]) (Predicate, error) {
	statement, err := subquerySelect(q)
	if err != nil {
		return Predicate{}, err
	}
	return Predicate{node: query.InSelect(left.node, statement), source: left.source}, nil
}

func NotInQuery[T comparable](left Expr[T], q Query[T]) (Predicate, error) {
	statement, err := subquerySelect(q)
	if err != nil {
		return Predicate{}, err
	}
	return Predicate{node: query.NotInSelect(left.node, statement), source: left.source}, nil
}

// SubqueryExpr lifts a scalar subquery into a NullExpr[T], the one entry
// point a scalar subquery reaches the typed expression language through.
// It returns NullExpr because a scalar subquery over zero rows is NULL
// regardless of what it selects, the same reasoning the aggregates above
// already apply to SUM, MAX and AVG. Unlike InQuery and ExistsQuery, which
// each build a whole Predicate, this returns a value, so it stands wherever
// a NullExpr does: naming a projected column in NullItem, such as
// NullItem("count", SubqueryExpr(countQuery), ...), or composed through
// CoalesceExpr into a plain Expr wherever one is required, such as
// GreaterExpr(amount, CoalesceExpr(SubqueryExpr(avgQuery), fallback)). It
// composes with Query.Correlated exactly like any other typed query, so q
// may read a column of the enclosing query, and the NullExpr it returns may
// itself be lifted a second time into an outer subquery — nesting is just
// building one Query[T] from another.
//
// It reads T from q's own projection the way InQuery does, so q and the
// context it is lifted into are checked against each other at compile time.
// q must project exactly one column, the restriction subquerySelect applies
// to every subquery standing in for a value.
func SubqueryExpr[T any](q Query[T]) (NullExpr[T], error) {
	statement, err := subquerySelect(q)
	if err != nil {
		return NullExpr[T]{}, err
	}
	return NullExpr[T]{node: query.Scalar(statement)}, nil
}

// CoalesceExpr returns value when it is not NULL, and fallback otherwise,
// the typed COALESCE. It takes a NullExpr but returns a plain Expr, because
// that is exactly what pairing a nullable expression with a fallback that is
// never NULL proves: the combined result can never be NULL either, so
// nothing downstream still needs to treat it as one.
//
// The returned Expr carries value's codec rather than fallback's. A
// fallback is ordinarily a bare literal built with Value, which has no
// codec of its own, while value is usually the column whose codec a later
// comparison against a Go value needs to encode against.
func CoalesceExpr[T any](value NullExpr[T], fallback Expr[T]) Expr[T] {
	bindErr := value.bindErr
	if bindErr == nil {
		bindErr = fallback.bindErr
	}
	return Expr[T]{
		node:    query.Coalesce(value.node, fallback.node),
		codec:   value.codec,
		source:  value.source,
		bindErr: bindErr,
	}
}

// AscResult orders by a projection's already-computed result rather than
// recomputing the expression behind it, which is what SELECT ... AS alias ...
// ORDER BY alias means. Pass the same ProjectionItem the projection was built
// from, so the alias is written once and renaming it cannot leave the ordering
// naming a result the query no longer produces.
//
// It takes a ProjectionItem rather than an Expr for the reason query.AscResult
// states: a result name is legal in exactly one place in SQL, alone as a whole
// ORDER BY term, and PostgreSQL rejects it anywhere else. Keeping it out of
// Expr means Where, GroupBy, Having and a join condition all refuse it at
// compile time.
func AscResult(item ProjectionItem) OrderTerm {
	return OrderTerm{result: &item}
}

// DescResult orders by a projection's result in descending order. It is
// AscResult reversed and carries every rule AscResult states.
func DescResult(item ProjectionItem) OrderTerm {
	return OrderTerm{result: &item, descending: true}
}

// ExistsQuery tests whether q returns any row. Write q so that it reads a
// column of the enclosing query, which is what makes the test say something
// about the row being tested; an EXISTS over a query that reads only its own
// tables reports whether a table is non-empty and answers the same for every
// row.
//
// Unlike InQuery this places no requirement on what q projects, because EXISTS
// counts rows and never reads a value out of one.
func ExistsQuery[R any](q Query[R]) (Predicate, error) {
	statement, err := subqueryStatement(q)
	if err != nil {
		return Predicate{}, err
	}
	return Predicate{node: query.Exists(statement)}, nil
}

// NotExistsQuery tests whether q returns no row at all. A NULL among q's
// results changes nothing, unlike in NotInQuery: EXISTS reports whether a row
// arrived and never reads a value from it.
func NotExistsQuery[R any](q Query[R]) (Predicate, error) {
	statement, err := subqueryStatement(q)
	if err != nil {
		return Predicate{}, err
	}
	return Predicate{node: query.NotExists(statement)}, nil
}

// subquerySelect lowers a typed query to the query package's Select so it can
// stand inside a predicate. A single projected column is required because a
// subquery compared against one value has to return one value.
func subquerySelect[R any](q Query[R]) (query.Select, error) {
	statement, err := subqueryStatement(q)
	if err != nil {
		return query.Select{}, err
	}
	if columns := q.Schema().Columns(); len(columns) != 1 {
		return query.Select{}, planError("invalid_query", "subquery.projection",
			"a subquery used as a value must project exactly one column")
	}
	return statement, nil
}

// subqueryStatement lowers a typed query to a Select without judging what it
// projects, which is all an EXISTS needs.
func subqueryStatement[R any](q Query[R]) (query.Select, error) {
	if err := q.Validate(); err != nil {
		return query.Select{}, err
	}
	body, err := queryBody(q.plan)
	if err != nil {
		return query.Select{}, err
	}
	statement, ok := body.(query.Select)
	if !ok {
		return query.Select{}, planError("unsupported_feature", "subquery",
			"a native query cannot stand inside a predicate")
	}
	return statement, nil
}
