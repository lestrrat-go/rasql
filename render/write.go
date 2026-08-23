package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/statement"
)

// Insert renders stmt for d.
func Insert(d dialect.Dialect, stmt query.Insert) (statement.Statement, error) {
	return renderStatement(d, "INSERT", stmt.Validate, func(renderer *renderer) error {
		return renderer.writeInsert(stmt)
	})
}

// Update renders stmt for d.
func Update(d dialect.Dialect, stmt query.Update) (statement.Statement, error) {
	return renderStatement(d, "UPDATE", stmt.Validate, func(renderer *renderer) error {
		return renderer.writeUpdate(stmt)
	})
}

// Delete renders stmt for d.
func Delete(d dialect.Dialect, stmt query.Delete) (statement.Statement, error) {
	return renderStatement(d, "DELETE", stmt.Validate, func(renderer *renderer) error {
		return renderer.writeDelete(stmt)
	})
}

// Upsert renders stmt for d.
func Upsert(d dialect.Dialect, stmt query.Upsert) (statement.Statement, error) {
	return renderStatement(d, "UPSERT", stmt.Validate, func(renderer *renderer) error {
		return renderer.writeUpsert(stmt)
	})
}

// Write renders a stmt that changes database rows.
func Write(d dialect.Dialect, stmt query.WriteStatement) (statement.Statement, error) {
	switch stmt := stmt.(type) {
	case query.Insert:
		return Insert(d, stmt)
	case query.Update:
		return Update(d, stmt)
	case query.Delete:
		return Delete(d, stmt)
	case query.Upsert:
		return Upsert(d, stmt)
	default:
		if stmt == nil {
			return statement.Statement{}, &Error{Err: fmt.Errorf("write statement must not be nil")}
		}
		return statement.Statement{}, &Error{Err: fmt.Errorf("unsupported write statement %T", stmt)}
	}
}

func (r *renderer) writeInsert(stmt query.Insert) error {
	if err := r.writeInsertBase(stmt); err != nil {
		return err
	}
	return r.writeReturning(stmt.Returning())
}

func (r *renderer) writeInsertBase(stmt query.Insert) error {
	table, err := r.quoteQualified(stmt.Into().Schema(), stmt.Into().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("INSERT INTO ")
	r.builder.WriteString(table)
	if stmt.UsesDefaultValues() {
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
	for i, column := range stmt.Columns() {
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
	for i, values := range stmt.Rows() {
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
// problems that apply to the same stmt, so none of these texts may be
// copied anywhere else.
const (
	upsertConflictTargetMessage = "explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget"
	upsertDefaultValuesMessage  = "default-values upsert is not supported"
	upsertNoAssignmentsMessage  = "upsert without assignments is not supported"
)

func (r *renderer) writeUpsert(stmt query.Upsert) error {
	style := r.dialect.UpsertStyle()
	if !r.dialect.Supports(dialect.CapabilityUpsert) || style == dialect.UpsertUnsupported {
		return fmt.Errorf("upsert is not supported")
	}
	defaultValuesRejected := stmt.Insert().UsesDefaultValues() && !r.dialect.Supports(dialect.CapabilityDefaultValuesUpsert)
	if len(stmt.ConflictColumns()) > 0 && !r.dialect.Supports(dialect.CapabilityConflictTarget) {
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
		if style == dialect.UpsertDuplicateKey && len(stmt.Assignments()) == 0 {
			problems = append(problems, upsertNoAssignmentsMessage)
		}
		return errors.New(strings.Join(problems, "; "))
	}
	if defaultValuesRejected {
		return errors.New(upsertDefaultValuesMessage)
	}
	if err := r.writeInsertBase(stmt.Insert()); err != nil {
		return err
	}
	switch style {
	case dialect.UpsertOnConflict:
		conflict := stmt.ConflictColumns()
		assignments := stmt.Assignments()
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
		assignments := stmt.Assignments()
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
	return r.writeReturning(stmt.Returning())
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

func (r *renderer) writeUpdate(stmt query.Update) error {
	if stmt.Where() == nil && !stmt.AllowsAll() {
		return fmt.Errorf("UPDATE requires a WHERE predicate or an explicit AllowAll")
	}
	table, err := r.quoteQualified(stmt.Table().Schema(), stmt.Table().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("UPDATE ")
	r.builder.WriteString(table)
	r.builder.WriteString(" SET ")
	for i, assignment := range stmt.Assignments() {
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
	if where := stmt.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}
	return r.writeReturning(stmt.Returning())
}

func (r *renderer) writeDelete(stmt query.Delete) error {
	if stmt.Where() == nil && !stmt.AllowsAll() {
		return fmt.Errorf("DELETE requires a WHERE predicate or an explicit AllowAll")
	}
	table, err := r.quoteQualified(stmt.From().Schema(), stmt.From().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("DELETE FROM ")
	r.builder.WriteString(table)
	if where := stmt.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}
	return r.writeReturning(stmt.Returning())
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
	column, ok := projection.Expression().(query.ColumnRef)
	if !ok {
		return r.writeProjection(projection)
	}
	name, err := r.quoteIdentifier(column.Name())
	if err != nil {
		return err
	}
	r.builder.WriteString(name)
	if projection.Alias() == "" {
		return nil
	}
	alias, err := r.quoteIdentifier(projection.Alias())
	if err != nil {
		return err
	}
	r.builder.WriteString(" AS ")
	r.builder.WriteString(alias)
	return nil
}
