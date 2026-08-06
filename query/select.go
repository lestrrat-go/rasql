package query

import "fmt"

// Projection selects an expression, optionally with an alias.
type Projection struct {
	expression Expression
	alias      string
}

// Project selects expression.
func Project(expression Expression) Projection {
	return Projection{expression: expression}
}

// As returns a copy of p with alias as its result name.
func (p Projection) As(alias string) Projection {
	p.alias = alias
	return p
}

// Expression returns the selected expression.
func (p Projection) Expression() Expression {
	return p.expression
}

// Alias returns the result alias, or an empty string when no alias is set.
func (p Projection) Alias() string {
	return p.alias
}

// JoinType identifies the supported join syntax.
type JoinType string

const (
	JoinInner JoinType = "INNER"
	JoinLeft  JoinType = "LEFT"
)

// Join adds a joined table to a SELECT statement.
type Join struct {
	kind   JoinType
	source Table
	on     Expression
}

// InnerJoin returns an INNER JOIN.
func InnerJoin(source Table, on Expression) Join {
	return Join{kind: JoinInner, source: source, on: on}
}

// LeftJoin returns a LEFT JOIN.
func LeftJoin(source Table, on Expression) Join {
	return Join{kind: JoinLeft, source: source, on: on}
}

// Type returns the join type.
func (j Join) Type() JoinType {
	return j.kind
}

// Source returns the joined table.
func (j Join) Source() Table {
	return j.source
}

// On returns the join condition.
func (j Join) On() Expression {
	return j.on
}

// Order specifies ordering for a result expression.
type Order struct {
	expression Expression
	descending bool
}

// Asc orders expression in ascending order.
func Asc(expression Expression) Order {
	return Order{expression: expression}
}

// Desc orders expression in descending order.
func Desc(expression Expression) Order {
	return Order{expression: expression, descending: true}
}

// Expression returns the ordered expression.
func (o Order) Expression() Expression {
	return o.expression
}

// Descending reports whether the order is descending.
func (o Order) Descending() bool {
	return o.descending
}

// Select is an immutable SELECT statement.
type Select struct {
	projections []Projection
	from        Table
	joins       []Join
	where       Expression
	groupBy     []Expression
	having      Expression
	orderBy     []Order
	limit       int
	hasLimit    bool
	offset      int
	hasOffset   bool
}

// NewSelect creates a validated SELECT statement.
func NewSelect(from Table, projections ...Projection) (Select, error) {
	statement := Select{
		from:        from,
		projections: append([]Projection(nil), projections...),
	}
	if err := statement.Validate(); err != nil {
		return Select{}, err
	}
	return statement, nil
}

// NewGroupedSelect creates a validated grouped SELECT statement.
// It is NewSelect for a statement that groups. The grouping has to be supplied
// here rather than added afterwards, because a grouped statement may project a
// column outside an aggregate beside one and an ungrouped statement may not, so
// NewSelect would refuse the projection set before WithGroupBy could make it
// legal. A grouping expression must not call an aggregate function and must not
// be a bare bound value.
func NewGroupedSelect(from Table, groupBy []Expression, projections ...Projection) (Select, error) {
	statement := Select{
		from:        from,
		groupBy:     append([]Expression(nil), groupBy...),
		projections: append([]Projection(nil), projections...),
	}
	if err := statement.Validate(); err != nil {
		return Select{}, err
	}
	return statement, nil
}

// WithJoin returns a copy of s with join appended.
func (s Select) WithJoin(join Join) (Select, error) {
	copy := s.clone()
	copy.joins = append(copy.joins, join)
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithWhere returns a copy of s with expression as its predicate.
func (s Select) WithWhere(expression Expression) (Select, error) {
	copy := s.clone()
	copy.where = expression
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithGroupBy returns a copy of s with expressions appended to its grouping.
// It refines a statement NewSelect already accepted, which is every statement
// whose projections either all aggregate or never aggregate. A projection set
// that mixes the two has to start from NewGroupedSelect, because NewSelect
// refuses it before this method can be called.
func (s Select) WithGroupBy(expressions ...Expression) (Select, error) {
	copy := s.clone()
	copy.groupBy = append(copy.groupBy, expressions...)
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithHaving returns a copy of s with expression as its grouped predicate,
// replacing any predicate set before it.
// HAVING filters groups after aggregation, so it may call an aggregate. It
// requires a statement that groups: either an explicit GROUP BY, or a
// projection set in which every projection aggregates, which is one group.
// Without a GROUP BY it follows the same rule as ORDER BY over an aggregating
// statement, and may read a column only inside an aggregate.
func (s Select) WithHaving(expression Expression) (Select, error) {
	copy := s.clone()
	copy.having = expression
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithOrder returns a copy of s with orders appended.
func (s Select) WithOrder(orders ...Order) (Select, error) {
	copy := s.clone()
	copy.orderBy = append(copy.orderBy, orders...)
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithLimit returns a copy of s with a result limit.
func (s Select) WithLimit(limit int) (Select, error) {
	copy := s.clone()
	copy.limit = limit
	copy.hasLimit = true
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithOffset returns a copy of s with a result offset.
func (s Select) WithOffset(offset int) (Select, error) {
	copy := s.clone()
	copy.offset = offset
	copy.hasOffset = true
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// Projections returns a copy of selected expressions.
func (s Select) Projections() []Projection {
	return append([]Projection(nil), s.projections...)
}

// From returns the statement's primary table.
func (s Select) From() Table {
	return s.from
}

// Joins returns a copy of joined tables.
func (s Select) Joins() []Join {
	return append([]Join(nil), s.joins...)
}

// Where returns the predicate, or nil when no predicate is set.
func (s Select) Where() Expression {
	return s.where
}

// GroupBy returns a copy of the grouping expressions.
func (s Select) GroupBy() []Expression {
	return append([]Expression(nil), s.groupBy...)
}

// Having returns the grouped predicate, or nil when none is set.
func (s Select) Having() Expression {
	return s.having
}

// OrderBy returns a copy of result ordering.
func (s Select) OrderBy() []Order {
	return append([]Order(nil), s.orderBy...)
}

// Limit returns the result limit and whether one is set.
func (s Select) Limit() (int, bool) {
	return s.limit, s.hasLimit
}

// Offset returns the result offset and whether one is set.
func (s Select) Offset() (int, bool) {
	return s.offset, s.hasOffset
}

// Validate reports whether s is internally consistent.
func (s Select) Validate() error {
	if err := s.from.validate(); err != nil {
		return validationError("from", "%s", err)
	}
	if len(s.projections) == 0 {
		return validationError("projections", "must not be empty")
	}

	sources := map[string]struct{}{s.from.key(): {}}
	for i, join := range s.joins {
		path := fmt.Sprintf("joins[%d]", i)
		if join.kind != JoinInner && join.kind != JoinLeft {
			return validationError(path+".type", "unsupported join type %q", join.kind)
		}
		if err := join.source.validate(); err != nil {
			return validationError(path+".source", "%s", err)
		}
		if _, exists := sources[join.source.key()]; exists {
			return validationError(path+".source", "duplicates table reference %q", join.source.Qualifier())
		}
		sources[join.source.key()] = struct{}{}
		if err := validateSelectClauseExpression(join.on, sources, "a JOIN ON condition", path+".on"); err != nil {
			return err
		}
	}

	grouped := len(s.groupBy) > 0
	for i, expression := range s.groupBy {
		path := fmt.Sprintf("group_by[%d]", i)
		if err := validateSelectClauseExpression(expression, sources, "a GROUP BY clause", path); err != nil {
			return err
		}
		if _, ok := expression.(Value); ok {
			return validationError(path, "must not be a bound value")
		}
	}

	projections, err := s.validateProjectionSet(sources, grouped)
	if err != nil {
		return err
	}
	if s.where != nil {
		if err := validateSelectClauseExpression(s.where, sources, "a WHERE clause", "where"); err != nil {
			return err
		}
	}
	if s.having != nil {
		if !grouped && !projections.aggregate {
			return validationError("having", "requires a GROUP BY clause or a projection set that aggregates")
		}
		usage, err := validateExpression(s.having, aggregateClauseContext(sources, "a HAVING clause"), "having")
		if err != nil {
			return err
		}
		if !grouped && usage.bareColumn {
			return validationError("having", "reads a column outside an aggregate function while the projections aggregate, which requires a GROUP BY clause")
		}
	}
	for i, order := range s.orderBy {
		if err := validateOrder(order, sources, projections, grouped, fmt.Sprintf("order_by[%d]", i)); err != nil {
			return err
		}
	}
	if s.hasLimit && s.limit < 0 {
		return validationError("limit", "must not be negative")
	}
	if s.hasOffset && s.offset < 0 {
		return validationError("offset", "must not be negative")
	}
	return nil
}

// validateOrder validates one ORDER BY expression against what the statement
// groups and what the projection set does, which projections reports. ORDER BY
// runs after aggregation. A statement that groups explicitly may read its
// grouping keys freely and may call an aggregate, so no bareColumn check
// applies. An ungrouped statement follows the projection set instead, which has
// exactly two cases because validateProjectionSet already refused the mixed
// set: projections that never aggregate leave one result row per source row, so
// ORDER BY reads columns freely and must not aggregate; projections that all
// aggregate leave a single implicit group, so ORDER BY may call an aggregate,
// while a column it reads outside every aggregate belongs to no row of that
// group and needs an explicit GROUP BY, exactly as in the projection set
// itself.
func validateOrder(order Order, sources map[string]struct{}, projections expressionUsage, grouped bool, path string) error {
	if grouped {
		_, err := validateExpression(order.expression, aggregateClauseContext(sources, "an ORDER BY clause"), path)
		return err
	}
	if !projections.aggregate {
		return validateSelectClauseExpression(order.expression, sources, "an ORDER BY clause", path)
	}
	usage, err := validateExpression(order.expression, aggregateClauseContext(sources, "an ORDER BY clause"), path)
	if err != nil {
		return err
	}
	if usage.bareColumn {
		return validationError(path, "reads a column outside an aggregate function while the projections aggregate, which requires a GROUP BY clause")
	}
	return nil
}

// validateProjectionSet validates every projection and the rules that span the
// set, and reports what the set as a whole reads so a later clause can apply the
// rules that depend on it. A statement that both aggregates and reads a column
// outside an aggregate needs GROUP BY to mean anything, so an ungrouped
// statement refuses the combination instead of rendering SQL no supported
// dialect answers usefully: PostgreSQL rejects the ungrouped column and SQLite
// pairs the aggregate with an arbitrary row. A grouped statement may mix the two
// freely; validation does not check that a column projected outside an
// aggregate appears among the grouping keys, and leaves that to the database,
// because a precise version of that check needs primary-key and outer-join
// reasoning this package does not have.
func (s Select) validateProjectionSet(sources map[string]struct{}, grouped bool) (expressionUsage, error) {
	var (
		total         expressionUsage
		aggregatePath string
		columnPath    string
	)
	for i, projection := range s.projections {
		path := fmt.Sprintf("projections[%d]", i)
		if projection.alias != "" {
			if err := validateAlias(projection.alias); err != nil {
				return expressionUsage{}, validationError(path+".alias", "%s", err)
			}
		}
		usage, err := validateExpression(projection.expression, projectionContext(sources), path+".expression")
		if err != nil {
			return expressionUsage{}, err
		}
		if usage.aggregate && aggregatePath == "" {
			aggregatePath = path
		}
		if usage.bareColumn && columnPath == "" {
			columnPath = path
		}
		total = total.merge(usage)
	}
	if !grouped && total.aggregate && total.bareColumn {
		return expressionUsage{}, validationError(columnPath+".expression", "reads a column outside an aggregate function while %s aggregates, which requires a GROUP BY clause", aggregatePath)
	}
	return total, nil
}

func (s Select) clone() Select {
	copy := s
	copy.projections = append([]Projection(nil), s.projections...)
	copy.joins = append([]Join(nil), s.joins...)
	copy.groupBy = append([]Expression(nil), s.groupBy...)
	copy.orderBy = append([]Order(nil), s.orderBy...)
	return copy
}
