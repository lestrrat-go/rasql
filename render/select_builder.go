package render

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/statement"
)

// SelectBuilder builds parameterized SQL through an immutable fluent API.
type SelectBuilder struct {
	dialect          dialect.Dialect
	from             query.TableRef
	projections      []query.Projection
	joins            []query.Join
	predicates       []query.Expression
	groupBy          []query.Expression
	havingPredicates []query.Expression
	orders           []query.Order
	limit            int
	hasLimit         bool
	offset           int
	hasOffset        bool
	distinct         bool
	err              error
}

// SelectFrom starts a fluent SELECT builder for d using from as its primary table.
func SelectFrom(d dialect.Dialect, from query.TableRef) SelectBuilder {
	return SelectBuilder{dialect: d, from: from}
}

// WithDialect returns a copy of b that renders for d.
// SelectFrom takes the dialect a builder starts with. WithDialect is for a
// caller that assembles the statement first and chooses the dialect where it
// renders, which is what the root package's builders do at their terminal call.
func (b SelectBuilder) WithDialect(d dialect.Dialect) SelectBuilder {
	b = b.clone()
	b.dialect = d
	return b
}

// Select adds columns from the primary table by name.
func (b SelectBuilder) Select(columns ...string) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	for _, name := range columns {
		column, err := b.from.Column(name)
		if err != nil {
			return b.withError(err)
		}
		b.projections = append(b.projections, query.Project(column))
	}
	return b
}

// Project adds projections created through the basic query API.
func (b SelectBuilder) Project(projections ...query.Projection) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.projections = append(b.projections, projections...)
	return b
}

// Join adds joins created through the basic query API.
func (b SelectBuilder) Join(joins ...query.Join) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.joins = append(b.joins, joins...)
	return b
}

// Where adds a predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made. Use one call
// with query.Or for a top-level OR.
func (b SelectBuilder) Where(expression query.Expression) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	if expression == nil {
		return b.withError(fmt.Errorf("WHERE expression must not be nil"))
	}
	b.predicates = append(b.predicates, expression)
	return b
}

// WhereEqual adds an equality predicate for a primary-table column and binds value.
// Repeated calls combine with AND in the order they were made, including calls to Where.
func (b SelectBuilder) WhereEqual(columnName string, value any) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	column, err := b.from.Column(columnName)
	if err != nil {
		return b.withError(err)
	}
	b.predicates = append(b.predicates, query.Equal(column, query.Bind(value)))
	return b
}

// WhereIn adds an IN predicate for a primary-table column and binds each value.
// Repeated calls combine with AND in the order they were made, including calls to
// Where and WhereEqual. Build reports an error when values is empty, because
// IN () is not valid SQL.
func (b SelectBuilder) WhereIn(columnName string, values ...any) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	if len(values) == 0 {
		return b.withError(fmt.Errorf("IN requires at least one value"))
	}
	column, err := b.from.Column(columnName)
	if err != nil {
		return b.withError(err)
	}
	binds := make([]query.Expression, len(values))
	for i, value := range values {
		binds[i] = query.Bind(value)
	}
	b.predicates = append(b.predicates, query.In(column, binds...))
	return b
}

// GroupBy adds grouping expressions created through the basic query API.
// Repeated calls append in the order they were made.
func (b SelectBuilder) GroupBy(expressions ...query.Expression) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.groupBy = append(b.groupBy, expressions...)
	return b
}

// GroupByColumns adds primary-table columns to the grouping by name.
// It is the untyped counterpart of passing a generated query.ColumnRef to GroupBy.
func (b SelectBuilder) GroupByColumns(names ...string) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	for _, name := range names {
		column, err := b.from.Column(name)
		if err != nil {
			return b.withError(err)
		}
		b.groupBy = append(b.groupBy, column)
	}
	return b
}

// Having adds a grouped predicate created through the basic query API.
// Repeated calls combine with AND in the order they were made, exactly as Where
// does. Use one call with query.Or for a top-level OR.
func (b SelectBuilder) Having(expression query.Expression) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	if expression == nil {
		return b.withError(fmt.Errorf("HAVING expression must not be nil"))
	}
	b.havingPredicates = append(b.havingPredicates, expression)
	return b
}

// Order adds ordering created through the basic query API.
func (b SelectBuilder) Order(orders ...query.Order) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.orders = append(b.orders, orders...)
	return b
}

// OrderAsc adds ascending ordering for a primary-table column.
func (b SelectBuilder) OrderAsc(columnName string) SelectBuilder {
	return b.orderColumn(columnName, false)
}

// OrderDesc adds descending ordering for a primary-table column.
func (b SelectBuilder) OrderDesc(columnName string) SelectBuilder {
	return b.orderColumn(columnName, true)
}

// Distinct de-duplicates the result rows.
func (b SelectBuilder) Distinct() SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.distinct = true
	return b
}

// Limit sets the maximum number of result rows.
func (b SelectBuilder) Limit(limit int) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.limit = limit
	b.hasLimit = true
	return b
}

// Offset sets the number of result rows to skip.
func (b SelectBuilder) Offset(offset int) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	b.offset = offset
	b.hasOffset = true
	return b
}

// Build validates b and returns its parameterized SQL statement.
func (b SelectBuilder) Build() (statement.Statement, error) {
	if b.err != nil {
		return statement.Statement{}, b.err
	}
	stmt, err := b.buildFromJoinsWhere(b.projections...)
	if err != nil {
		return statement.Statement{}, err
	}
	if b.distinct {
		stmt, err = stmt.WithDistinct()
		if err != nil {
			return statement.Statement{}, err
		}
	}
	if predicate, ok := combinePredicates(b.havingPredicates); ok {
		stmt, err = stmt.WithHaving(predicate)
		if err != nil {
			return statement.Statement{}, err
		}
	}
	if len(b.orders) > 0 {
		stmt, err = stmt.WithOrder(b.orders...)
		if err != nil {
			return statement.Statement{}, err
		}
	}
	if b.hasLimit {
		stmt, err = stmt.WithLimit(b.limit)
		if err != nil {
			return statement.Statement{}, err
		}
	}
	if b.hasOffset {
		stmt, err = stmt.WithOffset(b.offset)
		if err != nil {
			return statement.Statement{}, err
		}
	}
	return Select(b.dialect, stmt)
}

// BuildCount validates b and returns a parameterized statement that counts the
// rows b matches. It projects COUNT(*) under the result name "count" in place of
// b's projections, and drops ordering, which cannot change a count. It reports
// an error when b sets a limit or an offset, because a count of a paged
// statement is not the count the caller built the statement to ask for; when b
// groups, because a grouped count returns one row per group rather than the
// single row Count expects; when b sets a HAVING clause, because BuildCount
// discards the projections that clause was written against; and when b is
// distinct, because BuildCount replaces the projections with COUNT(*), which
// would turn SELECT DISTINCT a, b into SELECT DISTINCT COUNT(*) — always one
// row, never the count of distinct rows. There is no distinct-row count to
// offer in its place: query.Count(column).WithDistinct() renders
// COUNT(DISTINCT column), which counts the distinct non-NULL values of one
// column rather than the rows SELECT DISTINCT returns.
func (b SelectBuilder) BuildCount() (statement.Statement, error) {
	if b.err != nil {
		return statement.Statement{}, b.err
	}
	if b.hasLimit {
		return statement.Statement{}, fmt.Errorf("cannot count a statement with a limit")
	}
	if b.hasOffset {
		return statement.Statement{}, fmt.Errorf("cannot count a statement with an offset")
	}
	if len(b.groupBy) > 0 {
		return statement.Statement{}, fmt.Errorf("cannot count a grouped statement")
	}
	if len(b.havingPredicates) > 0 {
		return statement.Statement{}, fmt.Errorf("cannot count a statement with a HAVING clause")
	}
	if b.distinct {
		return statement.Statement{}, fmt.Errorf("cannot count a distinct statement")
	}
	stmt, err := b.buildFromJoinsWhere(query.Project(query.CountAll()).As("count"))
	if err != nil {
		return statement.Statement{}, err
	}
	return Select(b.dialect, stmt)
}

// buildFromJoinsWhere assembles a query.Select from b's from, joins, and
// accumulated predicates, using projections in place of b's own. Build and
// BuildCount share it so both carry every predicate, combined with AND in the
// order the calls were made, into the statement they build.
//
// It builds through query.NewJoinedSelect, which validates the joins, the
// grouping and the projections together. Each of those has to be present at the
// first validation: a projection or a grouping expression that reads a joined
// table is refused while the join is missing, and a projection set that mixes an
// aggregate with a bare column is refused while the grouping that makes it legal
// is missing. Only the clauses validation judges against an already complete
// statement, WHERE here and HAVING, ORDER BY, LIMIT and OFFSET in Build, are
// attached afterwards.
func (b SelectBuilder) buildFromJoinsWhere(projections ...query.Projection) (query.Select, error) {
	stmt, err := query.NewJoinedSelect(b.from, b.joins, b.groupBy, projections...)
	if err != nil {
		return query.Select{}, err
	}
	if predicate, ok := combinePredicates(b.predicates); ok {
		stmt, err = stmt.WithWhere(predicate)
		if err != nil {
			return query.Select{}, err
		}
	}
	return stmt, nil
}

func (b SelectBuilder) orderColumn(name string, descending bool) SelectBuilder {
	b = b.clone()
	if b.err != nil {
		return b
	}
	column, err := b.from.Column(name)
	if err != nil {
		return b.withError(err)
	}
	if descending {
		b.orders = append(b.orders, query.Desc(column))
		return b
	}
	b.orders = append(b.orders, query.Asc(column))
	return b
}

func (b SelectBuilder) withError(err error) SelectBuilder {
	if b.err == nil {
		b.err = err
	}
	return b
}

func (b SelectBuilder) clone() SelectBuilder {
	copy := b
	copy.projections = append([]query.Projection(nil), b.projections...)
	copy.joins = append([]query.Join(nil), b.joins...)
	copy.predicates = append([]query.Expression(nil), b.predicates...)
	copy.groupBy = append([]query.Expression(nil), b.groupBy...)
	copy.havingPredicates = append([]query.Expression(nil), b.havingPredicates...)
	copy.orders = append([]query.Order(nil), b.orders...)
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
