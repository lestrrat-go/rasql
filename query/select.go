package query

import (
	"fmt"

	"github.com/lestrrat-go/rasql/internal/nilcheck"
)

// Projection is one entry of a SELECT list or a RETURNING list: an expression
// and the result name it is reported under. ColumnRef implements it, so a
// column may be selected without a wrapper, and Project wraps any other
// expression.
//
// The interface carries no unexported method, so code outside this package may
// implement it. What such an implementation can express is still bounded:
// Expression is sealed by its own unexported method, so the only expressions it
// can return are ones this package built, and the alias it reports is checked
// by the statement's validation and again by the dialect when the statement
// renders.
//
// The methods are not named Expression and Alias. Order, Not, NullTest and
// Membership already have an Expression() Expression method and would satisfy
// the interface by accident, and TableRef.Alias names a table qualifier in the
// FROM clause rather than a result name.
type Projection interface {
	// ProjectedExpression returns the expression the projection selects.
	ProjectedExpression() Expression
	// ResultAlias returns the result name, or an empty string when the
	// projection is reported under whatever name the database picks for it.
	ResultAlias() string
}

// ExpressionProjection projects an arbitrary expression, optionally under a
// result name. Project builds it. A ColumnRef needs no wrapper, because it is a
// Projection itself.
type ExpressionProjection struct {
	expression Expression
	alias      string
}

// Project selects expression. Use it for what is not a plain column: an
// aggregate, a function call, a bound value, or a scalar subquery. A ColumnRef
// is already a Projection and is passed on its own. Project takes an
// Expression, unlike a comparison or a membership test, so a plain Go value
// projected on its own has to be wrapped with Bind here.
func Project(expression Expression) ExpressionProjection {
	return ExpressionProjection{expression: expression}
}

// As returns a copy of p reported under alias.
func (p ExpressionProjection) As(alias string) ExpressionProjection {
	p.alias = alias
	return p
}

// ProjectedExpression returns the selected expression.
func (p ExpressionProjection) ProjectedExpression() Expression {
	return p.expression
}

// ResultAlias returns the result alias, or an empty string when no alias is set.
func (p ExpressionProjection) ResultAlias() string {
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
	source TableRef
	on     Expression
}

// InnerJoin returns an INNER JOIN.
func InnerJoin(source TableRef, on Expression) Join {
	return Join{kind: JoinInner, source: source, on: on}
}

// LeftJoin returns a LEFT JOIN.
func LeftJoin(source TableRef, on Expression) Join {
	return Join{kind: JoinLeft, source: source, on: on}
}

// Type returns the join type.
func (j Join) Type() JoinType {
	return j.kind
}

// Source returns the joined table.
func (j Join) Source() TableRef {
	return j.source
}

// On returns the join condition.
func (j Join) On() Expression {
	return j.on
}

// Order specifies ordering for a result expression, or for the result a
// projection of the same statement already computed.
//
// A term is one or the other and never both. Asc and Desc build the expression
// form, and AscResult and DescResult build the projection form; Expression and
// ResultProjection report which one a value carries.
type Order struct {
	expression       Expression
	resultProjection Projection
	descending       bool
}

// Asc orders expression in ascending order.
func Asc(expression Expression) Order {
	return Order{expression: expression}
}

// Desc orders expression in descending order.
func Desc(expression Expression) Order {
	return Order{expression: expression, descending: true}
}

// AscResult orders by projection's already-computed result, in ascending
// order, instead of recomputing the expression behind it. It is what
// SELECT … AS alias … ORDER BY alias means, written so the alias is given
// once: projection is the same value passed to Project (or the same ColumnRef
// projected on its own), so a long function call or a scalar subquery is
// written once instead of twice, and renaming its alias with As cannot drift
// the two apart the way repeating the name as a second string could.
//
// projection is not an Expression, which is deliberate. A result name
// resolves against the statement's projections rather than against its
// tables, and SQL admits it in exactly one position: alone, as a whole
// ORDER BY term. PostgreSQL refuses ORDER BY alias || 'x' with "column does
// not exist" while MySQL and SQLite run it, so an expression node carrying a
// result name would build statements that work on two dialects and fail on
// the third. Keeping it a kind of Order instead means WithWhere, WithGroupBy,
// WithHaving, and a join condition all refuse it at compile time, since each
// of those takes an Expression.
//
// Statement validation resolves projection to the result name it reports —
// the name As gave it, or, for a column selected without a wrapper, the
// column's own name — and refuses a projection with neither, since an
// unaliased function call or Project has no name any dialect agrees on; give
// it one with As, or call Asc on the expression instead. Validation also
// refuses a name no projection of the statement reports, so the ordering
// cannot name a result the statement never produces, and a name more than one
// projection reports: PostgreSQL answers that shape with "ORDER BY is
// ambiguous" and MySQL with error 1052, while SQLite silently sorts by
// whichever projection was aliased, so refusing it here is what keeps one
// statement meaning one thing on all three. Membership and ambiguity are both
// judged by that resolved name, not by comparing projection against the
// statement's projections with ==, since a projection built by Project can
// hold an expression whose dynamic type is not comparable.
func AscResult(projection Projection) Order {
	return Order{resultProjection: projection}
}

// DescResult orders by projection's result in descending order. It is
// AscResult reversed, and carries every rule AscResult states.
func DescResult(projection Projection) Order {
	return Order{resultProjection: projection, descending: true}
}

// Expression returns the ordered expression, or nil when the order names a
// projection's result instead. ResultProjection reports which of the two an
// Order carries.
func (o Order) Expression() Expression {
	return o.expression
}

// ResultProjection returns the projection the order sorts by, and reports
// whether the order names one at all. It reports nil and false for an order
// built by Asc or Desc, whose ordering key is the Expression instead.
//
// It returns the projection itself rather than the name resolved from it, so
// that name is computed in one place, ResultName, instead of Order keeping a
// second copy that repeating alias could drift from — the exact drift
// AscResult and DescResult exist to rule out.
func (o Order) ResultProjection() (Projection, bool) {
	if nilcheck.Is(o.resultProjection) {
		return nil, false
	}
	return o.resultProjection, true
}

// Descending reports whether the order is descending.
func (o Order) Descending() bool {
	return o.descending
}

// Select is an immutable SELECT statement.
type Select struct {
	projections []Projection
	from        TableRef
	// correlations names the tables of an enclosing statement this one reads,
	// which WithCorrelation declares and Correlations reports. It is empty for
	// every statement that stands on its own.
	correlations []TableRef
	joins        []Join
	where        Expression
	groupBy      []Expression
	having       Expression
	orderBy      []Order
	limit        int
	hasLimit     bool
	offset       int
	hasOffset    bool
	distinct     bool
}

// NewSelect creates a validated SELECT statement.
func NewSelect(from TableRef, projections ...Projection) (Select, error) {
	return NewJoinedSelect(from, nil, nil, projections...)
}

// NewGroupedSelect creates a validated grouped SELECT statement.
// It is NewSelect for a statement that groups. The grouping has to be supplied
// here rather than added afterwards, because a grouped statement may project a
// column outside an aggregate beside one and an ungrouped statement may not, so
// NewSelect would refuse the projection set before WithGroupBy could make it
// legal. A grouping expression must not call an aggregate function and must not
// be a bare bound value.
func NewGroupedSelect(from TableRef, groupBy []Expression, projections ...Projection) (Select, error) {
	return NewJoinedSelect(from, nil, groupBy, projections...)
}

// NewJoinedSelect creates a validated SELECT statement that already carries its
// joins, and its grouping when it groups. NewSelect and NewGroupedSelect are it
// with no joins.
//
// Validation judges the statement as constructed, and every clause may read
// only a table the statement selects from. A join added afterwards through
// WithJoin therefore cannot rescue a projection or a grouping expression that
// reads the joined table, because validation has already refused it. Supply the
// joins here whenever any projection or grouping expression reads a joined
// table's column. Pass a nil groupBy for a statement that does not group.
func NewJoinedSelect(from TableRef, joins []Join, groupBy []Expression, projections ...Projection) (Select, error) {
	statement := Select{
		from:        from,
		joins:       append([]Join(nil), joins...),
		groupBy:     append([]Expression(nil), groupBy...),
		projections: append([]Projection(nil), projections...),
	}
	if err := statement.Validate(); err != nil {
		return Select{}, err
	}
	return statement, nil
}

// WithCorrelation returns a copy of s that may read the columns of tables, the
// tables of the statement s will be nested inside. It is what makes a
// correlated subquery buildable: Subquery states what correlation means, and
// this is where the statement says which enclosing tables it correlates with.
//
// Declare the correlation before the clause that reads the enclosing table.
// Every builder method validates the copy it returns, and a statement under
// construction has no enclosing statement to ask, so WithWhere refuses a
// predicate reading a table this statement has not been told about. That is the
// same ordering NewJoinedSelect already documents for a join a projection
// reads, and for the same reason.
//
// The declaration is checked at both ends rather than trusted. A table named
// here that the enclosing statement does not carry is refused when the subquery
// is nested, so a subquery attached to the wrong statement is reported instead
// of rendering SQL naming a table that is not there. render.Select refuses a
// statement that declares a correlation and is rendered on its own, since a
// subquery position is the only place the enclosing row it reads exists at all.
//
// The enclosing statement may be a SELECT, a DELETE, or an UPDATE. Each one has
// the row a correlated subquery reads: a result row for a SELECT, and the row
// being written for the other two. DELETE FROM users WHERE EXISTS (SELECT
// orders.id FROM orders WHERE orders.user_id = users.id) is the shape this
// enables, and PostgreSQL 17, MySQL 8.4 and SQLite all run it.
//
// tables is what this statement itself reads, and it is exactly what this
// statement gets: the enclosing statement's other tables stay out of scope. That
// is what keeps a subquery selecting from a table the enclosing statement also
// selects from legal, which is the shape render refuses for MySQL alone because
// of its error 1093 and the other two dialects run. Declaring a correlation with
// that same table is what makes both copies reachable, and validation then
// refuses the pair and names the alias that separates them.
//
// A statement between the reader and the table declares it too. Correlation
// reaching two levels out means the middle statement carries a subquery naming
// a table the middle statement has not been told about, and validating that
// middle statement on its own would refuse it, since nothing then says a third
// statement is coming. Repeating the declaration is what tells it, and it also
// leaves every level saying which enclosing tables the statements inside it
// read.
func (s Select) WithCorrelation(tables ...TableRef) (Select, error) {
	copy := s.clone()
	copy.correlations = append(copy.correlations, tables...)
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// Correlations returns a copy of the enclosing tables s declared with
// WithCorrelation. It is empty for a statement that stands on its own.
func (s Select) Correlations() []TableRef {
	return append([]TableRef(nil), s.correlations...)
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

// WithDistinct returns a copy of s that de-duplicates its result rows. It
// takes no argument because no With… method in this package unsets what it
// set, so a no-argument setter matches WithLimit and WithOffset.
//
// It places no rule of its own on ORDER BY. SQLite answers an ORDER BY term
// that is not among the projections of a distinct statement from an arbitrary
// surviving row, while PostgreSQL refuses the same statement with SQLSTATE
// 42P10 and MySQL with error 3065 ER_FIELD_IN_ORDER_NOT_SELECT. rasql renders
// what the caller asks for and lets the database report that error rather
// than refusing it here.
func (s Select) WithDistinct() (Select, error) {
	copy := s.clone()
	copy.distinct = true
	if err := copy.Validate(); err != nil {
		return Select{}, err
	}
	return copy, nil
}

// WithHaving returns a copy of s with expression as its grouped predicate,
// replacing any predicate set before it.
// HAVING filters groups after aggregation, so it may call an aggregate. It
// requires a statement that groups: either an explicit GROUP BY, or a
// projection set that aggregates and reads no column outside an aggregate,
// which is one group. Not every projection in that set has to aggregate: a
// projection that reads no column, a bound value for instance, may sit beside
// the aggregate. Without a GROUP BY the clause follows the same rule as ORDER BY
// over an aggregating statement, and may read a column only inside an aggregate.
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
func (s Select) From() TableRef {
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

// Distinct reports whether the statement de-duplicates its result rows.
func (s Select) Distinct() bool {
	return s.distinct
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
//
// A statement that declared a correlation with WithCorrelation is judged with
// those tables in scope, which is what lets a correlated subquery be built and
// validated before any statement encloses it. Nesting it checks the same
// declaration against the statement that really encloses it, and render.Select
// refuses one rendered on its own.
func (s Select) Validate() error {
	if err := s.from.validate(); err != nil {
		return validationError("from", "%s", err)
	}
	if len(s.projections) == 0 {
		return validationError("projections", "must not be empty")
	}

	sources := sourceScope{keys: make(map[string]struct{}, len(s.correlations)+len(s.joins)+1)}
	// A declared correlation goes into the scope before this statement's own
	// tables, so a table this statement selects from that it also declared is
	// refused: both would then be reachable, and a server would answer every
	// column reference from this statement's copy without the SQL saying which
	// was meant. The repair is the same alias validateSourceReference names for
	// two tables of one statement.
	for i, correlated := range s.correlations {
		path := fmt.Sprintf("correlations[%d]", i)
		if err := correlated.validate(); err != nil {
			return validationError(path, "%s", err)
		}
		if err := validateSourceReference(sources.references, correlated, path); err != nil {
			return err
		}
		sources.add(correlated)
	}
	if err := validateSourceReference(sources.references, s.from, "from"); err != nil {
		return err
	}
	sources.add(s.from)
	for i, join := range s.joins {
		path := fmt.Sprintf("joins[%d]", i)
		if join.kind != JoinInner && join.kind != JoinLeft {
			return validationError(path+".type", "unsupported join type %q", join.kind)
		}
		if err := join.source.validate(); err != nil {
			return validationError(path+".source", "%s", err)
		}
		// The duplicate message is about one statement listing the same table
		// twice, so it covers only the tables this statement selects from. A
		// join naming a table this statement declared a correlation with falls
		// through to validateSourceReference below, whose message names the
		// alias that separates the two scopes instead.
		if _, inScope := sources.keys[join.source.key()]; inScope && !isCorrelatedSource(s.correlations, join.source) {
			return validationError(path+".source", "duplicates table reference %q", join.source.QualifiedName())
		}
		if err := validateSourceReference(sources.references, join.source, path+".source"); err != nil {
			return err
		}
		sources.add(join.source)
		if err := validateSubqueryClauseExpression(join.on, sources, "a JOIN ON condition", path+".on"); err != nil {
			return err
		}
	}

	grouped := len(s.groupBy) > 0
	for i, expression := range s.groupBy {
		path := fmt.Sprintf("group_by[%d]", i)
		if err := validateSubqueryClauseExpression(expression, sources, "a GROUP BY clause", path); err != nil {
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
		if err := validateSubqueryClauseExpression(s.where, sources, "a WHERE clause", "where"); err != nil {
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
	var results map[string]int
	for _, order := range s.orderBy {
		if _, ok := order.ResultProjection(); ok {
			results = s.resultNames()
			break
		}
	}
	for i, order := range s.orderBy {
		if err := validateOrder(order, sources, projections, grouped, results, fmt.Sprintf("order_by[%d]", i)); err != nil {
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

// isCorrelatedSource reports whether source is one of the enclosing tables
// correlations declares. It exists so a join can tell a table this statement
// already selects from, which is a plain duplicate, from one it declared a
// correlation with, which is the ambiguity an alias repairs.
func isCorrelatedSource(correlations []TableRef, source TableRef) bool {
	for _, correlated := range correlations {
		if correlated.key() == source.key() {
			return true
		}
	}
	return false
}

// validateOrder validates one ORDER BY term. A term naming a projection's
// result, built by AscResult or DescResult, is resolved against results and
// never reaches the aggregate reasoning below: it names a projection
// validateProjectionSet has already judged under those rules, and a result
// name reads nothing of its own for them to apply to. Otherwise the term is an
// expression, validated against what the statement groups and what the
// projection set does, which projections reports. ORDER BY runs after
// aggregation. A statement that groups explicitly may read its grouping keys
// freely and may call an aggregate, so no bareColumn check applies. An
// ungrouped statement follows the projection set instead, which has exactly
// two cases because validateProjectionSet already refused the mixed set:
// projections that never aggregate leave one result row per source row, so
// ORDER BY reads columns freely and must not aggregate; projections that all
// aggregate leave a single implicit group, so ORDER BY may call an aggregate,
// while a column it reads outside every aggregate belongs to no row of that
// group and needs an explicit GROUP BY, exactly as in the projection set
// itself.
func validateOrder(order Order, sources sourceScope, projections expressionUsage, grouped bool, results map[string]int, path string) error {
	if projection, ok := order.ResultProjection(); ok {
		return validateOrderResultAlias(projection, results, path)
	}
	if grouped {
		_, err := validateExpression(order.expression, aggregateClauseContext(sources, "an ORDER BY clause"), path)
		return err
	}
	if !projections.aggregate {
		return validateSubqueryClauseExpression(order.expression, sources, "an ORDER BY clause", path)
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
func (s Select) validateProjectionSet(sources sourceScope, grouped bool) (expressionUsage, error) {
	var (
		total         expressionUsage
		aggregatePath string
		columnPath    string
	)
	for i, projection := range s.projections {
		path := fmt.Sprintf("projections[%d]", i)
		if nilcheck.Is(projection) {
			return expressionUsage{}, validationError(path, "must not be nil")
		}
		if alias := projection.ResultAlias(); alias != "" {
			if err := validateAlias(alias); err != nil {
				return expressionUsage{}, validationError(path+".alias", "%s", err)
			}
		}
		usage, err := validateExpression(projection.ProjectedExpression(), projectionContext(sources), path+".expression")
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

// ResultName returns the name p is reported under in a SELECT list: the alias
// As gave it, or, for a ColumnRef selected without a wrapper, the column's own
// name. It reports false for a projection with neither, which is every other
// unaliased projection: PostgreSQL, MySQL, and SQLite each pick a different
// name for an unaliased function call, so this package cannot say what such a
// projection is reported under and does not guess. AscResult, DescResult, and
// this statement's own rendering both resolve a projection's name through
// this one function, so the two can never compute it differently.
func ResultName(p Projection) (string, bool) {
	if alias := p.ResultAlias(); alias != "" {
		return alias, true
	}
	if column, ok := p.(ColumnRef); ok {
		return column.Name(), true
	}
	return "", false
}

// resultNames counts the result names this statement's projections report, so an
// ORDER BY term naming one can be resolved against them. The count rather than a
// presence flag is what lets validation tell an unknown name from an ambiguous
// one.
func (s Select) resultNames() map[string]int {
	names := make(map[string]int, len(s.projections))
	for _, projection := range s.projections {
		if name, ok := ResultName(projection); ok {
			names[name]++
		}
	}
	return names
}

func (s Select) clone() Select {
	copy := s
	copy.correlations = append([]TableRef(nil), s.correlations...)
	copy.projections = append([]Projection(nil), s.projections...)
	copy.joins = append([]Join(nil), s.joins...)
	copy.groupBy = append([]Expression(nil), s.groupBy...)
	copy.orderBy = append([]Order(nil), s.orderBy...)
	return copy
}
