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
		if err := validateClauseExpression(join.on, sources, "a JOIN ON condition", path+".on"); err != nil {
			return err
		}
	}

	if err := s.validateProjectionSet(sources); err != nil {
		return err
	}
	if s.where != nil {
		if err := validateClauseExpression(s.where, sources, "a WHERE clause", "where"); err != nil {
			return err
		}
	}
	for i, order := range s.orderBy {
		if err := validateClauseExpression(order.expression, sources, "an ORDER BY clause", fmt.Sprintf("order_by[%d]", i)); err != nil {
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

// validateProjectionSet validates every projection and the rules that span the
// set. A statement that both aggregates and reads a column outside an aggregate
// needs GROUP BY to mean anything, and GROUP BY is unsupported, so no such
// statement has a rendering any supported dialect answers usefully: PostgreSQL
// rejects the ungrouped column and SQLite pairs the aggregate with an arbitrary
// row. Validation refuses the combination instead.
func (s Select) validateProjectionSet(sources map[string]struct{}) error {
	var (
		total         expressionUsage
		aggregatePath string
		columnPath    string
	)
	for i, projection := range s.projections {
		path := fmt.Sprintf("projections[%d]", i)
		if projection.alias != "" {
			if err := validateAlias(projection.alias); err != nil {
				return validationError(path+".alias", "%s", err)
			}
		}
		usage, err := validateExpression(projection.expression, projectionContext(sources), path+".expression")
		if err != nil {
			return err
		}
		if usage.aggregate && aggregatePath == "" {
			aggregatePath = path
		}
		if usage.bareColumn && columnPath == "" {
			columnPath = path
		}
		total = total.merge(usage)
	}
	if total.aggregate && total.bareColumn {
		return validationError(columnPath+".expression", "reads a column outside an aggregate function while %s aggregates, which requires the unsupported GROUP BY", aggregatePath)
	}
	return nil
}

func (s Select) clone() Select {
	copy := s
	copy.projections = append([]Projection(nil), s.projections...)
	copy.joins = append([]Join(nil), s.joins...)
	copy.orderBy = append([]Order(nil), s.orderBy...)
	return copy
}
