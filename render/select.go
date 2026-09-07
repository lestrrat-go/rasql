// Package render converts validated query models into parameterized SQL.
package render

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

// Error describes a failure while rendering SQL.
type Error struct {
	Dialect string
	Err     error
}

var ErrUnsupportedSelectLock = errors.New("render: unsupported SELECT row lock")

type UnsupportedSelectLockError struct {
	Dialect string
	Clause  string
}

func (e *UnsupportedSelectLockError) Error() string {
	return fmt.Sprintf("the %s dialect cannot express SELECT %s", e.Dialect, e.Clause)
}

func (e *UnsupportedSelectLockError) Unwrap() error { return ErrUnsupportedSelectLock }

func (e *Error) Error() string {
	if e.Dialect == "" {
		return fmt.Sprintf("render: %s", e.Err)
	}
	return fmt.Sprintf("render %s: %s", e.Dialect, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// ErrUnsupportedMatchOperator is the sentinel wrapped by every
// [UnsupportedMatchOperatorError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedMatchOperator = errors.New("render: unsupported MATCH operator")

var ErrUnsupportedAggregateFilter = errors.New("render: unsupported aggregate FILTER")

type UnsupportedAggregateFilterError struct{ Dialect string }

func (e *UnsupportedAggregateFilterError) Error() string {
	return fmt.Sprintf("the %s dialect cannot express aggregate FILTER", e.Dialect)
}

func (e *UnsupportedAggregateFilterError) Unwrap() error { return ErrUnsupportedAggregateFilter }

// UnsupportedMatchOperatorError reports that a statement compares an
// expression with [query.OperatorMatch] against a dialect that has not been
// granted [dialect.CapabilityMatchOperator]. query.Validate accepts MATCH
// like any other query.BinaryOperator, because the operator's shape is the
// same on every dialect; only SQLite can actually run it, since MATCH is
// answered by a virtual table's own module rather than by SQL itself, so
// this package refuses to render it for any other dialect instead of
// sending SQL the target database does not understand.
type UnsupportedMatchOperatorError struct {
	// Dialect is the name of the dialect that cannot express MATCH.
	Dialect string
}

func (e *UnsupportedMatchOperatorError) Error() string {
	return fmt.Sprintf("the %s dialect cannot express MATCH: it has no full-text search operator", e.Dialect)
}

// Unwrap exposes ErrUnsupportedMatchOperator so
// errors.Is(err, ErrUnsupportedMatchOperator) works alongside errors.As
// against *UnsupportedMatchOperatorError.
func (e *UnsupportedMatchOperatorError) Unwrap() error {
	return ErrUnsupportedMatchOperator
}

// Select renders s for d.
//
// It refuses a statement that declared a correlation with
// query.Select.WithCorrelation. Such a statement reads a column of the row an
// enclosing statement is on, which exists only while the statement runs inside
// another one, so rendering it here would emit SQL naming a table the FROM
// clause never lists. query.Validate accepts it, because the statement is a
// consistent model of a subquery; this is the same division the MATCH operator
// already follows, where validation accepts the shape and rendering refuses the
// dialects that cannot run it.
func Select(d dialect.Dialect, s query.Select) (stmt.Statement, error) {
	return renderStatement(d, "SELECT", s.Validate, func(renderer *renderer) error {
		if correlations := s.Correlations(); len(correlations) > 0 {
			return fmt.Errorf("the statement declares a correlation with table %q and is rendered on its own; a correlated SELECT is only valid inside the statement it correlates with, as the statement given to query.Exists, query.NotExists, query.Scalar, query.InSelect or query.NotInSelect", correlations[0].QualifiedName())
		}
		return renderer.writeSelect(s)
	})
}

// Result renders a reusable result query without changing its relational
// shape. The supplied metadata is validated by query.ResultOf; the body is
// emitted directly so ordering and paging remain attached to the body.
func Result(d dialect.Dialect, s query.ResultQuery) (stmt.Statement, error) {
	return renderStatement(d, "SELECT", func() error {
		if s.Body() == nil {
			return fmt.Errorf("result query body must not be empty")
		}
		if err := s.Body().Validate(); err != nil {
			return err
		}
		return nil
	}, func(r *renderer) error {
		if body := s.Body(); body != nil {
			if correlations := bodyCorrelations(body); len(correlations) > 0 {
				return fmt.Errorf("the statement declares a correlation with table %q and is rendered on its own", correlations[0].QualifiedName())
			}
			return r.writeQueryBody(body)
		}
		return fmt.Errorf("result query body must not be empty")
	})
}

func bodyCorrelations(body query.QueryBody) []query.RelationRef {
	if s, ok := body.(query.Select); ok {
		return s.Correlations()
	}
	if s, ok := body.(*query.Select); ok && s != nil {
		return s.Correlations()
	}
	return nil
}

func renderStatement(d dialect.Dialect, operation string, validate func() error, write func(*renderer) error) (stmt.Statement, error) {
	if isNilDialect(d) {
		return stmt.Statement{}, &Error{Err: fmt.Errorf("dialect must not be nil")}
	}
	if err := validate(); err != nil {
		return stmt.Statement{}, &Error{Dialect: d.Name(), Err: fmt.Errorf("invalid %s statement: %w", operation, err)}
	}
	renderer := renderer{dialect: d}
	if err := write(&renderer); err != nil {
		return stmt.Statement{}, &Error{Dialect: d.Name(), Err: err}
	}
	return stmt.New(sqltext.Text(renderer.builder.String()), renderer.args...), nil
}

type renderer struct {
	dialect dialect.Dialect
	builder strings.Builder
	args    []any
	// excludedStyle is the dialect's upsert conflict-handling syntax for the
	// duration of an upsert conflict-update assignment, and inExcluded
	// reports whether the expression walk currently sits inside one.
	// writeUpsertAssignments sets both around each assignment's value, so an
	// ExcludedColumn renders correctly however deep inside that value it sits
	// — not only at the value's own top level. EXCLUDED means nothing outside
	// a conflict-update assignment, so writeExcludedColumn refuses one
	// reached anywhere else.
	excludedStyle           dialect.UpsertStyle
	inExcluded              bool
	cteScope                []query.CTE
	expressionValidator     func(query.Expression) error
	compilerExpressionDepth int
}

type compilerEmitter struct {
	renderer *renderer
}

func (e compilerEmitter) WriteSQL(sql string) {
	e.renderer.builder.WriteString(sql)
}

func (e compilerEmitter) Identifier(parts ...string) error {
	var quoted []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		value, err := e.renderer.quoteIdentifier(part)
		if err != nil {
			return err
		}
		quoted = append(quoted, value)
	}
	if len(quoted) == 0 {
		return fmt.Errorf("identifier must contain a non-empty component")
	}
	e.renderer.builder.WriteString(strings.Join(quoted, "."))
	return nil
}

func (e compilerEmitter) Argument(value any) error {
	return e.renderer.writeArgument(value)
}

func (e compilerEmitter) Expression(expression query.Expression) error {
	if e.renderer.expressionValidator != nil {
		if err := e.renderer.expressionValidator(expression); err != nil {
			return err
		}
	}
	return e.renderer.writeExpression(expression)
}

func (r *renderer) compiler() dialect.Compiler {
	provider, ok := r.dialect.(dialect.CompilerProvider)
	if !ok {
		return nil
	}
	return provider.Compiler()
}

func (r *renderer) writeSelect(s query.Select) error {
	previousValidator := r.expressionValidator
	defer func() { r.expressionValidator = previousValidator }()
	previous := r.cteScope
	owned := append([]query.CTE(nil), previous...)
	owned = append(owned, s.CTEs()...)
	r.cteScope = owned
	defer func() { r.cteScope = previous }()
	if err := r.validateSelectSources(s); err != nil {
		return err
	}
	if ctes := s.CTEs(); len(ctes) > 0 {
		r.builder.WriteString("WITH ")
		for i, cte := range ctes {
			if i > 0 {
				r.builder.WriteString(", ")
			}
			name, err := r.quoteIdentifier(cte.Name())
			if err != nil {
				return err
			}
			r.builder.WriteString(name)
			r.builder.WriteString(" AS (")
			if err := r.writeQueryBody(cte.Query().Body()); err != nil {
				return err
			}
			r.builder.WriteByte(')')
		}
		r.builder.WriteByte(' ')
	}
	r.builder.WriteString("SELECT ")
	if s.Distinct() {
		r.builder.WriteString("DISTINCT ")
	}
	for i, projection := range s.Projections() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		r.expressionValidator = func(expression query.Expression) error {
			return s.ValidateCompilerExpression(expression, "projection", i)
		}
		if err := r.writeProjection(projection); err != nil {
			return err
		}
	}
	r.expressionValidator = nil

	r.builder.WriteString(" FROM ")
	if err := r.writeTable(s.From()); err != nil {
		return err
	}
	for i, join := range s.Joins() {
		r.builder.WriteByte(' ')
		r.builder.WriteString(string(join.Type()))
		r.builder.WriteString(" JOIN ")
		if err := r.writeTable(join.Source()); err != nil {
			return err
		}
		r.builder.WriteString(" ON ")
		r.expressionValidator = func(expression query.Expression) error {
			return s.ValidateCompilerExpression(expression, "join", i)
		}
		if err := r.writeExpression(join.On()); err != nil {
			return err
		}
	}
	r.expressionValidator = nil
	if where := s.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		r.expressionValidator = func(expression query.Expression) error {
			return s.ValidateCompilerExpression(expression, "where", -1)
		}
		if err := r.writeExpression(where); err != nil {
			return err
		}
		r.expressionValidator = nil
	}

	groupBy := s.GroupBy()
	if len(groupBy) > 0 {
		r.builder.WriteString(" GROUP BY ")
		for i, expression := range groupBy {
			if i > 0 {
				r.builder.WriteString(", ")
			}
			r.expressionValidator = func(expression query.Expression) error {
				return s.ValidateCompilerExpression(expression, "group", i)
			}
			if err := r.writeExpression(expression); err != nil {
				return err
			}
		}
		r.expressionValidator = nil
	}
	if having := s.Having(); having != nil {
		r.builder.WriteString(" HAVING ")
		r.expressionValidator = func(expression query.Expression) error {
			return s.ValidateCompilerExpression(expression, "having", -1)
		}
		if err := r.writeExpression(having); err != nil {
			return err
		}
		r.expressionValidator = nil
	}

	orders := s.OrderBy()
	if len(orders) > 0 {
		r.builder.WriteString(" ORDER BY ")
		for i, order := range orders {
			if i > 0 {
				r.builder.WriteString(", ")
			}
			if projection, ok := order.ResultProjection(); ok {
				// Validate ran ahead of write (renderStatement calls it
				// before this method), so this projection's name is already
				// confirmed present and unambiguous among s.Projections();
				// ResultName resolves it the same way that check did.
				name, _ := query.ResultName(projection)
				quoted, err := r.quoteIdentifier(name)
				if err != nil {
					return err
				}
				r.builder.WriteString(quoted)
			} else {
				r.expressionValidator = func(expression query.Expression) error {
					return s.ValidateCompilerExpression(expression, "order", i)
				}
				if err := r.writeExpression(order.Expression()); err != nil {
					return err
				}
				r.expressionValidator = nil
			}
			if order.Descending() {
				r.builder.WriteString(" DESC")
			}
		}
	}
	if compiler := r.compiler(); compiler != nil {
		limit, hasLimit := s.Limit()
		offset, hasOffset := s.Offset()
		return compiler.CompilePagination(compilerEmitter{renderer: r}, dialect.Pagination{
			Limit: limit, Offset: offset, HasLimit: hasLimit, HasOffset: hasOffset,
		})
	}
	if limit, ok := s.Limit(); ok {
		r.builder.WriteString(" LIMIT ")
		if err := r.writeArgument(limit); err != nil {
			return err
		}
	}
	if offset, ok := s.Offset(); ok {
		r.builder.WriteString(" OFFSET ")
		if err := r.writeArgument(offset); err != nil {
			return err
		}
	}
	if lock, ok := s.Lock(); ok {
		if err := r.writeLock(lock); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) writeLock(lock query.Lock) error {
	strength := lock.Strength()
	baseCapability := dialect.CapabilitySelectForUpdate
	clause := "FOR UPDATE"
	switch strength {
	case query.LockNoKeyUpdate:
		if r.dialect.Name() != "postgresql" {
			return &UnsupportedSelectLockError{Dialect: r.dialect.Name(), Clause: "FOR NO KEY UPDATE"}
		}
		clause = "FOR NO KEY UPDATE"
	case query.LockShare:
		baseCapability = dialect.CapabilitySelectForShare
		clause = "FOR SHARE"
	case query.LockKeyShare:
		if r.dialect.Name() != "postgresql" {
			return &UnsupportedSelectLockError{Dialect: r.dialect.Name(), Clause: "FOR KEY SHARE"}
		}
		baseCapability = dialect.CapabilitySelectForShare
		clause = "FOR KEY SHARE"
	}
	if !r.dialect.Supports(baseCapability) {
		return &UnsupportedSelectLockError{Dialect: r.dialect.Name(), Clause: clause}
	}
	if len(lock.Tables()) > 0 && !r.dialect.Supports(dialect.CapabilitySelectLockOf) {
		return &UnsupportedSelectLockError{Dialect: r.dialect.Name(), Clause: "FOR ... OF"}
	}
	switch lock.WaitMode() {
	case query.LockWaitNoWait:
		if !r.dialect.Supports(dialect.CapabilitySelectLockNoWait) {
			return &UnsupportedSelectLockError{Dialect: r.dialect.Name(), Clause: "FOR ... NOWAIT"}
		}
	case query.LockWaitSkipLocked:
		if !r.dialect.Supports(dialect.CapabilitySelectLockSkipLocked) {
			return &UnsupportedSelectLockError{Dialect: r.dialect.Name(), Clause: "FOR ... SKIP LOCKED"}
		}
	}
	r.builder.WriteByte(' ')
	r.builder.WriteString(clause)
	if tables := lock.Tables(); len(tables) > 0 {
		r.builder.WriteString(" OF ")
		for i, table := range tables {
			if i > 0 {
				r.builder.WriteString(", ")
			}
			name, err := r.quoteIdentifier(table.Qualifier())
			if err != nil {
				return err
			}
			r.builder.WriteString(name)
		}
	}
	switch lock.WaitMode() {
	case query.LockWaitNoWait:
		r.builder.WriteString(" NOWAIT")
	case query.LockWaitSkipLocked:
		r.builder.WriteString(" SKIP LOCKED")
	}
	return nil
}

func (r *renderer) writeQueryBody(body query.QueryBody) error {
	switch body := body.(type) {
	case query.Select:
		return r.writeSelect(body)
	case *query.Select:
		if body == nil {
			return fmt.Errorf("unsupported query body %T", body)
		}
		return r.writeSelect(*body)
	case query.Compound:
		return r.writeCompound(body)
	case *query.Compound:
		if body == nil {
			return fmt.Errorf("unsupported query body %T", body)
		}
		return r.writeCompound(*body)
	default:
		return fmt.Errorf("unsupported query body %T", body)
	}
}

func (r *renderer) writeCompound(compound query.Compound) error {
	parenthesizeOperands := r.dialect.Name() != "sqlite"
	if parenthesizeOperands {
		r.builder.WriteByte('(')
	}
	if err := r.writeQueryBody(compound.Left().Body()); err != nil {
		return err
	}
	if parenthesizeOperands {
		r.builder.WriteByte(')')
	}
	r.builder.WriteByte(' ')
	switch compound.Operator() {
	case query.Union:
		r.builder.WriteString("UNION")
	case query.UnionAll:
		r.builder.WriteString("UNION ALL")
	case query.Intersect:
		r.builder.WriteString("INTERSECT")
	case query.Except:
		r.builder.WriteString("EXCEPT")
	default:
		return fmt.Errorf("unsupported compound operator %d", compound.Operator())
	}
	if parenthesizeOperands {
		r.builder.WriteString(" (")
	} else {
		r.builder.WriteByte(' ')
	}
	if err := r.writeQueryBody(compound.Right().Body()); err != nil {
		return err
	}
	if parenthesizeOperands {
		r.builder.WriteByte(')')
	}
	return nil
}

type visibleSource struct {
	qualifier  string
	schema     string
	descriptor string
}

func visibleSourceFromTable(table query.RelationRef) visibleSource {
	return visibleSource{
		qualifier:  table.Qualifier(),
		schema:     table.QualifierSchema(),
		descriptor: table.Definition().QualifiedName(),
	}
}

func (r *renderer) validateSelectSources(s query.Select) error {
	localCTEs := s.CTEs()
	ctes := append(append([]query.CTE(nil), r.cteScope...), localCTEs...)
	for i, left := range localCTEs {
		for j := 0; j < i; j++ {
			if dialect.IdentifiersEqual(r.dialect, localCTEs[j].Name(), left.Name()) {
				return fmt.Errorf("CTE names %q and %q collide in the %s dialect", localCTEs[j].Name(), left.Name(), r.dialect.Name())
			}
		}
	}
	sources := make([]visibleSource, 0, len(s.Correlations())+1+len(s.Joins()))
	add := func(table query.RelationRef) error {
		if cteName := table.CTEName(); cteName != "" {
			found := false
			for _, cte := range ctes {
				if cte.Name() == cteName && cte.Identity() == table.CTEIdentity() {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("CTE %q is not defined by this SELECT", cteName)
			}
		}
		candidate := visibleSourceFromTable(table)
		for _, existing := range sources {
			if !r.sourceIdentifiersConflict(existing, candidate) {
				continue
			}
			return fmt.Errorf("sources %q and %q share the rendered qualifier %q in the %s dialect; use a distinct alias", existing.descriptor, candidate.descriptor, candidate.qualifier, r.dialect.Name())
		}
		sources = append(sources, candidate)
		return nil
	}
	for _, correlation := range s.Correlations() {
		if err := add(correlation); err != nil {
			return err
		}
	}
	if err := add(s.From()); err != nil {
		return err
	}
	for _, join := range s.Joins() {
		if err := add(join.Source()); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) sourceIdentifiersConflict(left, right visibleSource) bool {
	if !dialect.IdentifiersEqual(r.dialect, left.qualifier, right.qualifier) {
		return false
	}
	if left.schema != "" && right.schema != "" {
		return dialect.IdentifiersEqual(r.dialect, left.schema, right.schema)
	}
	return true
}

func (r *renderer) writeTable(table query.RelationRef) error {
	if physical, ok := table.Table(); ok {
		return r.writePhysicalTable(physical)
	}
	if cte := table.CTEName(); cte != "" {
		name, err := r.quoteIdentifier(cte)
		if err != nil {
			return err
		}
		r.builder.WriteString(name)
		if table.Alias() != cte {
			r.builder.WriteString(" AS ")
			alias, err := r.quoteIdentifier(table.Alias())
			if err != nil {
				return err
			}
			r.builder.WriteString(alias)
		}
		return nil
	}
	if table.Alias() == "" {
		return fmt.Errorf("relation %q must have an alias", table.QualifiedName())
	}
	if body := table.ResultBody(); body != nil {
		r.builder.WriteByte('(')
		if err := r.writeQueryBody(body); err != nil {
			return err
		}
		r.builder.WriteString(") AS ")
		alias, err := r.quoteIdentifier(table.Alias())
		if err != nil {
			return err
		}
		r.builder.WriteString(alias)
		return nil
	}
	return fmt.Errorf("unsupported relation %q", table.QualifiedName())
}

func (r *renderer) writePhysicalTable(table query.TableRef) error {
	name, err := r.quoteQualified(table.Schema(), table.Name())
	if err != nil {
		return err
	}
	r.builder.WriteString(name)
	if table.Alias() == "" {
		return nil
	}
	alias, err := r.quoteIdentifier(table.Alias())
	if err != nil {
		return err
	}
	r.builder.WriteString(" AS ")
	r.builder.WriteString(alias)
	return nil
}

func (r *renderer) writeProjection(projection query.Projection) error {
	if err := r.writeExpression(projection.ProjectedExpression()); err != nil {
		return err
	}
	if projection.ResultAlias() == "" {
		return nil
	}
	alias, err := r.quoteIdentifier(projection.ResultAlias())
	if err != nil {
		return err
	}
	r.builder.WriteString(" AS ")
	r.builder.WriteString(alias)
	return nil
}

func (r *renderer) writeExpression(expression query.Expression) error {
	if compiler := r.compiler(); compiler != nil {
		if r.compilerExpressionDepth >= maxCompilerExpressionDepth {
			return errors.New("compiler expression delegation exceeds 64 nested callbacks")
		}
		r.compilerExpressionDepth++
		defer func() { r.compilerExpressionDepth-- }()
		handled, err := compiler.CompileExpression(compilerEmitter{renderer: r}, expression)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
	}
	return r.writeExpressionBuiltin(expression)
}

const maxCompilerExpressionDepth = 64

func (r *renderer) writeExpressionBuiltin(expression query.Expression) error {
	switch expression := expression.(type) {
	case query.ColumnRef:
		qualifier, err := r.quoteQualified(expression.Source().QualifierSchema(), expression.Source().Qualifier())
		if err != nil {
			return err
		}
		name, err := r.quoteIdentifier(expression.Name())
		if err != nil {
			return err
		}
		r.builder.WriteString(qualifier)
		r.builder.WriteByte('.')
		r.builder.WriteString(name)
		return nil
	case query.TableIdentifier:
		name, err := r.quoteIdentifier(expression.Table().Qualifier())
		if err != nil {
			return err
		}
		r.builder.WriteString(name)
		return nil
	case query.Value:
		return r.writeArgument(expression.Argument())
	case query.Function:
		r.builder.WriteString(string(expression.Name()))
		r.builder.WriteByte('(')
		if expression.Star() {
			r.builder.WriteByte('*')
		} else {
			if expression.Distinct() {
				r.builder.WriteString("DISTINCT ")
			}
			for i, argument := range expression.Arguments() {
				if i > 0 {
					r.builder.WriteString(", ")
				}
				if err := r.writeExpression(argument); err != nil {
					return err
				}
			}
		}
		r.builder.WriteByte(')')
		return nil
	case query.Binary:
		if expression.Operator() == query.OperatorMatch && !r.dialect.Supports(dialect.CapabilityMatchOperator) {
			return &UnsupportedMatchOperatorError{Dialect: r.dialect.Name()}
		}
		r.builder.WriteByte('(')
		if err := r.writeExpression(expression.Left()); err != nil {
			return err
		}
		r.builder.WriteByte(' ')
		r.builder.WriteString(string(expression.Operator()))
		r.builder.WriteByte(' ')
		if err := r.writeExpression(expression.Right()); err != nil {
			return err
		}
		r.builder.WriteByte(')')
		return nil
	case query.Case:
		r.builder.WriteString("(CASE")
		if operand := expression.Operand(); operand != nil {
			r.builder.WriteByte(' ')
			if err := r.writeExpression(operand); err != nil {
				return err
			}
		}
		for _, branch := range expression.Branches() {
			r.builder.WriteString(" WHEN ")
			if err := r.writeExpression(branch.Predicate()); err != nil {
				return err
			}
			r.builder.WriteString(" THEN ")
			if err := r.writeExpression(branch.Result()); err != nil {
				return err
			}
		}
		if fallback, ok := expression.Fallback(); ok {
			r.builder.WriteString(" ELSE ")
			if err := r.writeExpression(fallback); err != nil {
				return err
			}
		}
		r.builder.WriteString(" END)")
		return nil
	case query.Cast:
		r.builder.WriteString("CAST(")
		if err := r.writeExpression(expression.Expression()); err != nil {
			return err
		}
		r.builder.WriteString(" AS ")
		typeName, err := r.dialect.TypeName(schema.ColumnDef{Type: expression.Target()})
		if err != nil {
			return err
		}
		r.builder.WriteString(typeName)
		r.builder.WriteByte(')')
		return nil
	case query.Filter:
		if !r.dialect.Supports(dialect.CapabilityAggregateFilter) {
			return &UnsupportedAggregateFilterError{Dialect: r.dialect.Name()}
		}
		if err := r.writeExpression(expression.Aggregate()); err != nil {
			return err
		}
		r.builder.WriteString(" FILTER (WHERE ")
		if err := r.writeExpression(expression.Predicate()); err != nil {
			return err
		}
		r.builder.WriteByte(')')
		return nil
	case query.Over:
		if err := r.writeExpression(expression.Expression()); err != nil {
			return err
		}
		r.builder.WriteString(" OVER (")
		window := expression.Window()
		if partition := window.Partition(); len(partition) > 0 {
			r.builder.WriteString("PARTITION BY ")
			for i, part := range partition {
				if i > 0 {
					r.builder.WriteString(", ")
				}
				if err := r.writeExpression(part); err != nil {
					return err
				}
			}
		}
		if orders := window.Order(); len(orders) > 0 {
			if len(window.Partition()) > 0 {
				r.builder.WriteByte(' ')
			}
			r.builder.WriteString("ORDER BY ")
			for i, order := range orders {
				if i > 0 {
					r.builder.WriteString(", ")
				}
				if err := r.writeExpression(order.Expression()); err != nil {
					return err
				}
				if order.Descending() {
					r.builder.WriteString(" DESC")
				}
			}
		}
		r.builder.WriteString(")")
		return nil
	case query.TrustedFragment:
		parts := expression.Parts()
		fragments := strings.Split(expression.SQL(), "{}")
		for i, fragment := range fragments {
			r.builder.WriteString(fragment)
			if i >= len(parts) {
				continue
			}
			switch part := parts[i].(type) {
			case interface{ ValueExpression() query.Expression }:
				if err := r.writeExpression(part.ValueExpression()); err != nil {
					return err
				}
			case interface{ IdentifierValue() query.Identifier }:
				quoted, err := r.quoteIdentifier(part.IdentifierValue().Name())
				if err != nil {
					return err
				}
				r.builder.WriteString(quoted)
			default:
				return fmt.Errorf("unsupported fragment part %T", part)
			}
		}
		return nil
	case query.Logical:
		r.builder.WriteByte('(')
		for i, child := range expression.Expressions() {
			if i > 0 {
				r.builder.WriteByte(' ')
				r.builder.WriteString(string(expression.Operator()))
				r.builder.WriteByte(' ')
			}
			if err := r.writeExpression(child); err != nil {
				return err
			}
		}
		r.builder.WriteByte(')')
		return nil
	case query.Not:
		r.builder.WriteString("(NOT ")
		if err := r.writeExpression(expression.Expression()); err != nil {
			return err
		}
		r.builder.WriteByte(')')
		return nil
	case query.NullTest:
		r.builder.WriteByte('(')
		if err := r.writeExpression(expression.Expression()); err != nil {
			return err
		}
		r.builder.WriteString(" IS ")
		if expression.Not() {
			r.builder.WriteString("NOT ")
		}
		r.builder.WriteString("NULL)")
		return nil
	case query.Membership:
		if subquery, ok := expression.Subquery(); ok {
			_, hasLimit := subquery.Statement().Limit()
			_, hasOffset := subquery.Statement().Offset()
			if (hasLimit || hasOffset) && !r.dialect.Supports(dialect.CapabilitySubqueryLimit) {
				return fmt.Errorf("a subquery used with IN must not set LIMIT or OFFSET")
			}
			r.builder.WriteByte('(')
			if err := r.writeExpression(expression.Expression()); err != nil {
				return err
			}
			if expression.Not() {
				r.builder.WriteString(" NOT IN ")
			} else {
				r.builder.WriteString(" IN ")
			}
			if err := r.writeExpression(subquery); err != nil {
				return err
			}
			r.builder.WriteByte(')')
			return nil
		}
		values := expression.Values()
		if len(values) == 0 {
			return fmt.Errorf("IN requires at least one value")
		}
		r.builder.WriteByte('(')
		if err := r.writeExpression(expression.Expression()); err != nil {
			return err
		}
		if expression.Not() {
			r.builder.WriteString(" NOT IN (")
		} else {
			r.builder.WriteString(" IN (")
		}
		for i, value := range values {
			if i > 0 {
				r.builder.WriteString(", ")
			}
			if err := r.writeExpression(value); err != nil {
				return err
			}
		}
		r.builder.WriteString("))")
		return nil
	case query.Subquery:
		r.builder.WriteByte('(')
		if err := r.writeSelect(expression.Statement()); err != nil {
			return err
		}
		r.builder.WriteByte(')')
		return nil
	case query.Existence:
		// The outer parentheses match query.Not's, which wraps a prefix
		// operator the same way; the inner ones come from the query.Subquery
		// arm above, so EXISTS never has to parenthesize the SELECT itself.
		// No dialect capability gates a LIMIT here, unlike the query.Membership
		// arm: MySQL's error 1235 names LIMIT in an IN/ALL/ANY/SOME subquery,
		// and MySQL 8.4 runs EXISTS (SELECT … LIMIT 1) as PostgreSQL and SQLite
		// do.
		if expression.Not() {
			r.builder.WriteString("(NOT EXISTS ")
		} else {
			r.builder.WriteString("(EXISTS ")
		}
		if err := r.writeExpression(expression.Subquery()); err != nil {
			return err
		}
		r.builder.WriteByte(')')
		return nil
	case query.ExcludedColumn:
		return r.writeExcludedColumn(expression)
	default:
		return fmt.Errorf("unsupported expression %T", expression)
	}
}

// writeExcludedColumn renders an ExcludedColumn reached anywhere in an
// expression tree, not only at the top level of an upsert conflict-update
// assignment's value: validation admits ExcludedColumn nested inside another
// expression, such as Equal(Excluded(col), Bind(v)) or
// Coalesce(Excluded(col), Bind(0)), the same way it admits Column there, so
// the renderer has to recognise it at any depth to match. EXCLUDED means
// nothing outside a conflict-update assignment, so a call reached while
// inExcluded is false — anywhere else validation happened to accept one —
// fails loudly instead of silently.
func (r *renderer) writeExcludedColumn(excluded query.ExcludedColumn) error {
	if !r.inExcluded {
		return fmt.Errorf("references the excluded column %q outside an upsert conflict-update assignment", excluded.Column().Name())
	}
	name, err := r.quoteIdentifier(excluded.Column().Name())
	if err != nil {
		return err
	}
	switch r.excludedStyle {
	case dialect.UpsertOnConflict:
		r.builder.WriteString("EXCLUDED.")
		r.builder.WriteString(name)
		return nil
	case dialect.UpsertDuplicateKey:
		r.builder.WriteString("VALUES(")
		r.builder.WriteString(name)
		r.builder.WriteByte(')')
		return nil
	default:
		return fmt.Errorf("excluded columns are not supported")
	}
}

func (r *renderer) writeArgument(value any) error {
	placeholder, err := r.dialect.Placeholder(len(r.args) + 1)
	if err != nil {
		return fmt.Errorf("placeholder: %w", err)
	}
	r.builder.WriteString(placeholder)
	r.args = append(r.args, value)
	return nil
}

func (r *renderer) quoteIdentifier(name string) (string, error) {
	quoted, err := r.dialect.QuoteIdentifier(name)
	if err != nil {
		return "", fmt.Errorf("identifier %q: %w", name, err)
	}
	return quoted, nil
}

// quoteQualified quotes schemaName and name as two separate identifiers
// joined by a dot. It never quotes a dotted string as one identifier and
// never splits a string on a dot, so the dialect validates and quotes each
// segment on its own. An empty schemaName yields exactly what
// quoteIdentifier(name) yields.
func (r *renderer) quoteQualified(schemaName string, name string) (string, error) {
	quotedName, err := r.quoteIdentifier(name)
	if err != nil {
		return "", err
	}
	if schemaName == "" {
		return quotedName, nil
	}
	quotedSchema, err := r.quoteIdentifier(schemaName)
	if err != nil {
		return "", err
	}
	return quotedSchema + "." + quotedName, nil
}

func isNilDialect(d dialect.Dialect) bool {
	if d == nil {
		return true
	}
	value := reflect.ValueOf(d)
	return value.Kind() == reflect.Pointer && value.IsNil()
}
