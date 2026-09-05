package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
)

// ErrSubqueryReadsWriteTarget is the sentinel wrapped by every
// [SubqueryReadsWriteTargetError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrSubqueryReadsWriteTarget = errors.New("render: a write statement's subquery reads the target table")

// SubqueryReadsWriteTargetError reports that a subquery in a DELETE or an
// UPDATE reads the table the statement writes to, on a dialect that has not
// been granted [dialect.CapabilityWriteSubqueryTarget].
//
// query.Delete.Validate and query.Update.Validate both accept the statement,
// because the shape is ordinary SQL: PostgreSQL 17 and SQLite both run DELETE
// FROM t WHERE id IN (SELECT id FROM t …) and UPDATE t SET n = (SELECT MAX(n)
// FROM t). MySQL 8.4 answers error 1093, "You can't specify target table 't'
// for update in FROM clause", so this package refuses to render the statement
// for MySQL instead of sending SQL the server would reject. See
// dialect.CapabilityWriteSubqueryTarget for the shapes MySQL was measured
// against.
type SubqueryReadsWriteTargetError struct {
	// Dialect is the name of the dialect that cannot run the statement.
	Dialect string
	// Operation is the SQL keyword naming the statement, "DELETE" or
	// "UPDATE", so the message says which one the caller wrote.
	Operation string
	// Table names the statement's target table, as query.TableRef.QualifiedName
	// spells it for an error message.
	Table string
}

func (e *SubqueryReadsWriteTargetError) Error() string {
	return fmt.Sprintf("the %s dialect cannot run a %s whose subquery reads the target table %q: MySQL answers error 1093, \"You can't specify target table for update in FROM clause\". Run the SELECT as a statement of its own and pass the rows it returns to query.In, or point the subquery at a table other than the target", e.Dialect, e.Operation, e.Table)
}

// Unwrap exposes ErrSubqueryReadsWriteTarget so
// errors.Is(err, ErrSubqueryReadsWriteTarget) works alongside errors.As
// against *SubqueryReadsWriteTargetError.
func (e *SubqueryReadsWriteTargetError) Unwrap() error {
	return ErrSubqueryReadsWriteTarget
}

// Insert renders s for d.
func Insert(d dialect.Dialect, s query.Insert) (stmt.Statement, error) {
	return renderStatement(d, "INSERT", s.Validate, func(renderer *renderer) error {
		return renderer.writeInsert(s)
	})
}

// Update renders s for d.
func Update(d dialect.Dialect, s query.Update) (stmt.Statement, error) {
	return renderStatement(d, "UPDATE", s.Validate, func(renderer *renderer) error {
		return renderer.writeUpdate(s)
	})
}

// Delete renders s for d.
func Delete(d dialect.Dialect, s query.Delete) (stmt.Statement, error) {
	return renderStatement(d, "DELETE", s.Validate, func(renderer *renderer) error {
		return renderer.writeDelete(s)
	})
}

// Upsert renders s for d.
func Upsert(d dialect.Dialect, s query.Upsert) (stmt.Statement, error) {
	return renderStatement(d, "UPSERT", s.Validate, func(renderer *renderer) error {
		return renderer.writeUpsert(s)
	})
}

// Write renders a s that changes database rows.
func Write(d dialect.Dialect, s query.WriteStatement) (stmt.Statement, error) {
	switch s := s.(type) {
	case query.Insert:
		return Insert(d, s)
	case query.Update:
		return Update(d, s)
	case query.Delete:
		return Delete(d, s)
	case query.Upsert:
		return Upsert(d, s)
	default:
		if s == nil {
			return stmt.Statement{}, &Error{Err: fmt.Errorf("write statement must not be nil")}
		}
		return stmt.Statement{}, &Error{Err: fmt.Errorf("unsupported write statement %T", s)}
	}
}

func (r *renderer) writeInsert(s query.Insert) error {
	if err := r.writeInsertBase(s); err != nil {
		return err
	}
	return r.writeReturning(s.Returning())
}

func (r *renderer) writeInsertBase(s query.Insert) error {
	table, err := r.quoteQualified(s.Into().Schema(), s.Into().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("INSERT INTO ")
	r.builder.WriteString(table)
	if s.UsesDefaultValues() {
		switch {
		case r.dialect.Supports(dialect.CapabilityDefaultValues):
			r.builder.WriteString(" DEFAULT VALUES")
		case r.dialect.Supports(dialect.CapabilityEmptyInsert):
			r.builder.WriteString(" () VALUES ()")
		default:
			return fmt.Errorf("default-values INSERT is not supported")
		}
		return nil
	}
	r.builder.WriteString(" (")
	for i, column := range s.Columns() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		name, err := r.quoteIdentifier(column.Name())
		if err != nil {
			return err
		}
		r.builder.WriteString(name)
	}
	r.builder.WriteString(") VALUES ")
	for i, values := range s.Rows() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		r.builder.WriteByte('(')
		for j, value := range values {
			if j > 0 {
				r.builder.WriteString(", ")
			}
			if err := r.writeExpression(value); err != nil {
				return err
			}
		}
		r.builder.WriteByte(')')
	}
	return nil
}

// Each upsertMessage constant below is the only definition of its error text.
// The conflict-target check in writeUpsert joins its own message with the other
// problems that apply to the same s, so none of these texts may be
// copied anywhere else.
const (
	upsertConflictTargetMessage = "explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget"
	upsertDefaultValuesMessage  = "default-values upsert is not supported"
	upsertNoAssignmentsMessage  = "upsert without assignments is not supported"
)

func (r *renderer) writeUpsert(s query.Upsert) error {
	style := r.dialect.UpsertStyle()
	if !r.dialect.Supports(dialect.CapabilityUpsert) || style == dialect.UpsertUnsupported {
		return fmt.Errorf("upsert is not supported")
	}
	defaultValuesRejected := s.Insert().UsesDefaultValues() && !r.dialect.Supports(dialect.CapabilityDefaultValuesUpsert)
	if len(s.ConflictColumns()) > 0 && !r.dialect.Supports(dialect.CapabilityConflictTarget) {
		// This check runs before the default-values check and the style switch on
		// purpose: an explicit conflict target is unusable on this dialect for any
		// insert and any assignment list, while the other two problems are
		// conditional. A default-values upsert is only rejected by a dialect
		// lacking dialect.CapabilityDefaultValuesUpsert, and zero assignments is
		// only rejected by the ON DUPLICATE KEY style. Report every problem that
		// applies so a caller is not misled into thinking the target is the only
		// thing to fix and then hitting a second failure after removing it.
		problems := []string{upsertConflictTargetMessage}
		if defaultValuesRejected {
			problems = append(problems, upsertDefaultValuesMessage)
		}
		if style == dialect.UpsertDuplicateKey && len(s.Assignments()) == 0 {
			problems = append(problems, upsertNoAssignmentsMessage)
		}
		return errors.New(strings.Join(problems, "; "))
	}
	if defaultValuesRejected {
		return errors.New(upsertDefaultValuesMessage)
	}
	if err := r.writeInsertBase(s.Insert()); err != nil {
		return err
	}
	switch style {
	case dialect.UpsertOnConflict:
		conflict := s.ConflictColumns()
		assignments := s.Assignments()
		if len(conflict) == 0 && len(assignments) > 0 {
			return fmt.Errorf("upsert update requires a conflict target")
		}
		r.builder.WriteString(" ON CONFLICT")
		if len(conflict) > 0 {
			r.builder.WriteString(" (")
			for i, column := range conflict {
				if i > 0 {
					r.builder.WriteString(", ")
				}
				name, err := r.quoteIdentifier(column.Name())
				if err != nil {
					return err
				}
				r.builder.WriteString(name)
			}
			r.builder.WriteByte(')')
		}
		if len(assignments) == 0 {
			r.builder.WriteString(" DO NOTHING")
		} else {
			r.builder.WriteString(" DO UPDATE SET ")
			if err := r.writeUpsertAssignments(assignments, style); err != nil {
				return err
			}
		}
	case dialect.UpsertDuplicateKey:
		assignments := s.Assignments()
		if len(assignments) == 0 {
			return errors.New(upsertNoAssignmentsMessage)
		}
		r.builder.WriteString(" ON DUPLICATE KEY UPDATE ")
		if err := r.writeUpsertAssignments(assignments, style); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported upsert style %d", style)
	}
	return r.writeReturning(s.Returning())
}

// writeUpsertAssignments renders each assignment's value through the ordinary
// writeExpression, having first told the renderer which upsert style is in
// effect: writeExcludedColumn reads that state to render an ExcludedColumn
// wherever in the value it sits, and rejects one reached outside this scope.
func (r *renderer) writeUpsertAssignments(assignments []query.Assignment, style dialect.UpsertStyle) error {
	r.inExcluded = true
	r.excludedStyle = style
	defer func() { r.inExcluded = false }()
	for i, assignment := range assignments {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		column, err := r.quoteIdentifier(assignment.Column().Name())
		if err != nil {
			return err
		}
		r.builder.WriteString(column)
		r.builder.WriteString(" = ")
		if err := r.writeExpression(assignment.Value()); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) writeUpdate(s query.Update) error {
	if s.Where() == nil && !s.AllowsAll() {
		return fmt.Errorf("UPDATE requires a WHERE predicate or an explicit AllowAll")
	}
	// Both clauses are checked, because MySQL answers 1093 to either: a
	// subquery reading the target from the WHERE clause and one reading it
	// from a SET assignment's value are refused alike.
	if !r.dialect.Supports(dialect.CapabilityWriteSubqueryTarget) {
		if where := s.Where(); where != nil && expressionReadsWriteTarget(where, s.Table()) {
			return &SubqueryReadsWriteTargetError{Dialect: r.dialect.Name(), Operation: "UPDATE", Table: s.Table().QualifiedName()}
		}
		for _, assignment := range s.Assignments() {
			if expressionReadsWriteTarget(assignment.Value(), s.Table()) {
				return &SubqueryReadsWriteTargetError{Dialect: r.dialect.Name(), Operation: "UPDATE", Table: s.Table().QualifiedName()}
			}
		}
	}
	table, err := r.quoteQualified(s.Table().Schema(), s.Table().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("UPDATE ")
	r.builder.WriteString(table)
	r.builder.WriteString(" SET ")
	for i, assignment := range s.Assignments() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		column, err := r.quoteIdentifier(assignment.Column().Name())
		if err != nil {
			return err
		}
		r.builder.WriteString(column)
		r.builder.WriteString(" = ")
		if err := r.writeExpression(assignment.Value()); err != nil {
			return err
		}
	}
	if where := s.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}
	return r.writeReturning(s.Returning())
}

func (r *renderer) writeDelete(s query.Delete) error {
	if s.Where() == nil && !s.AllowsAll() {
		return fmt.Errorf("DELETE requires a WHERE predicate or an explicit AllowAll")
	}
	if where := s.Where(); where != nil && !r.dialect.Supports(dialect.CapabilityWriteSubqueryTarget) && expressionReadsWriteTarget(where, s.From()) {
		return &SubqueryReadsWriteTargetError{Dialect: r.dialect.Name(), Operation: "DELETE", Table: s.From().QualifiedName()}
	}
	table, err := r.quoteQualified(s.From().Schema(), s.From().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("DELETE FROM ")
	r.builder.WriteString(table)
	if where := s.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}
	return r.writeReturning(s.Returning())
}

// expressionReadsWriteTarget reports whether any subquery reachable from
// expression reads target in its own FROM or one of its joins. writeDelete
// calls it on the DELETE's predicate, and writeUpdate on the UPDATE's
// predicate and on each SET assignment's value, for a dialect without
// dialect.CapabilityWriteSubqueryTarget, which today is MySQL alone.
//
// The walk descends the whole expression rather than checking only a top-level
// query.Membership, because MySQL's error 1093 does not care where the
// subquery sits: it answered 1093 to an IN, a NOT IN, a scalar comparison
// against (SELECT MAX(id) FROM t), a subquery reading the target through a
// join, and a subquery nested one level inside another subquery. Every node
// that can carry a child expression is therefore listed here; the leaves —
// query.ColumnRef, query.Value, query.TableIdentifier, query.ExcludedColumn —
// carry none and fall through to false.
func expressionReadsWriteTarget(expression query.Expression, target query.TableRef) bool {
	switch expression := expression.(type) {
	case query.Subquery:
		return selectReadsWriteTarget(expression.Statement(), target)
	case query.Existence:
		// EXISTS carries its subquery in a node of its own rather than as a
		// query.Subquery operand, so it needs its own arm: without one the walk
		// would fall through to false and rasql would send MySQL a DELETE FROM
		// t WHERE EXISTS (SELECT … FROM t …) to answer 1093 to.
		return selectReadsWriteTarget(expression.Subquery().Statement(), target)
	case query.Binary:
		return expressionReadsWriteTarget(expression.Left(), target) || expressionReadsWriteTarget(expression.Right(), target)
	case query.Logical:
		for _, child := range expression.Expressions() {
			if expressionReadsWriteTarget(child, target) {
				return true
			}
		}
		return false
	case query.Not:
		return expressionReadsWriteTarget(expression.Expression(), target)
	case query.NullTest:
		return expressionReadsWriteTarget(expression.Expression(), target)
	case query.Membership:
		if subquery, ok := expression.Subquery(); ok && selectReadsWriteTarget(subquery.Statement(), target) {
			return true
		}
		if expressionReadsWriteTarget(expression.Expression(), target) {
			return true
		}
		for _, value := range expression.Values() {
			if expressionReadsWriteTarget(value, target) {
				return true
			}
		}
		return false
	case query.Function:
		for _, argument := range expression.Arguments() {
			if expressionReadsWriteTarget(argument, target) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// selectReadsWriteTarget reports whether statement reads target, either as one
// of its own sources or through a subquery of its own at any depth.
//
// A source matches on schema and name alone, ignoring the alias: MySQL answered
// 1093 to DELETE FROM t WHERE id IN (SELECT id FROM t AS ta …) exactly as it did
// to the unaliased form, so an alias hides nothing from the server and must
// hide nothing from this check either.
func selectReadsWriteTarget(statement query.Select, target query.TableRef) bool {
	if sameBaseTable(statement.From(), target) {
		return true
	}
	for _, join := range statement.Joins() {
		if sameBaseTable(join.Source(), target) || expressionReadsWriteTarget(join.On(), target) {
			return true
		}
	}
	for _, projection := range statement.Projections() {
		if expressionReadsWriteTarget(projection.ProjectedExpression(), target) {
			return true
		}
	}
	if where := statement.Where(); where != nil && expressionReadsWriteTarget(where, target) {
		return true
	}
	for _, key := range statement.GroupBy() {
		if expressionReadsWriteTarget(key, target) {
			return true
		}
	}
	if having := statement.Having(); having != nil && expressionReadsWriteTarget(having, target) {
		return true
	}
	for _, order := range statement.OrderBy() {
		// An order built by AscResult or DescResult carries a nil Expression
		// and names a projection instead, and that projection is one of the
		// statement's own, already walked above.
		if expression := order.Expression(); expression != nil && expressionReadsWriteTarget(expression, target) {
			return true
		}
	}
	return false
}

// sameBaseTable reports whether source and target name the same underlying
// table. It never compares the alias, for the reason selectReadsWriteTarget
// states.
//
// The schema is compared only when both sides state one. An unqualified name
// resolves against whatever schema the connection is currently using, which
// this package does not know, so a bare users and a qualified app.users may
// well be one table: MySQL answered 1093 to a DELETE from an unqualified
// target whose subquery named the same table schema-qualified, and it is that
// answer this has to predict. Treating the pair as the same table refuses a
// statement MySQL might have run, when the two really do live in different
// schemas; sending it and collecting 1093 from the server would be the other
// error, and this one at least names the repair. query.sourceReference.conflicts
// resolves the same question the same way, for the same reason, when it decides
// whether two sources of one statement are distinguishable.
func sameBaseTable(source query.TableRef, target query.TableRef) bool {
	if source.Name() != target.Name() {
		return false
	}
	if source.Schema() != "" && target.Schema() != "" {
		return source.Schema() == target.Schema()
	}
	return true
}

func (r *renderer) writeReturning(projections []query.Projection) error {
	if len(projections) == 0 {
		return nil
	}
	if !r.dialect.Supports(dialect.CapabilityReturning) {
		return fmt.Errorf("RETURNING is not supported")
	}
	r.builder.WriteString(" RETURNING ")
	for i, projection := range projections {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		if err := r.writeReturningProjection(projection); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) writeReturningProjection(projection query.Projection) error {
	column, ok := projection.ProjectedExpression().(query.ColumnRef)
	if !ok {
		return r.writeProjection(projection)
	}
	name, err := r.quoteIdentifier(column.Name())
	if err != nil {
		return err
	}
	r.builder.WriteString(name)
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
