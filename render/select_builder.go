package render

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
)

// SelectBuilder builds parameterized SQL through an immutable fluent API.
type SelectBuilder struct {
	dialect     dialect.Dialect
	from        query.Table
	projections []query.Projection
	joins       []query.Join
	predicates  []query.Expression
	orders      []query.Order
	limit       int
	hasLimit    bool
	offset      int
	hasOffset   bool
	err         error
}

// SelectFrom starts a fluent SELECT builder for d using from as its primary table.
func SelectFrom(d dialect.Dialect, from query.Table) SelectBuilder {
	return SelectBuilder{dialect: d, from: from}
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
func (b SelectBuilder) Build() (Statement, error) {
	if b.err != nil {
		return Statement{}, b.err
	}
	statement, err := query.NewSelect(b.from, b.projections...)
	if err != nil {
		return Statement{}, err
	}
	for _, join := range b.joins {
		statement, err = statement.WithJoin(join)
		if err != nil {
			return Statement{}, err
		}
	}
	if predicate, ok := combinePredicates(b.predicates); ok {
		statement, err = statement.WithWhere(predicate)
		if err != nil {
			return Statement{}, err
		}
	}
	if len(b.orders) > 0 {
		statement, err = statement.WithOrder(b.orders...)
		if err != nil {
			return Statement{}, err
		}
	}
	if b.hasLimit {
		statement, err = statement.WithLimit(b.limit)
		if err != nil {
			return Statement{}, err
		}
	}
	if b.hasOffset {
		statement, err = statement.WithOffset(b.offset)
		if err != nil {
			return Statement{}, err
		}
	}
	return Select(b.dialect, statement)
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
