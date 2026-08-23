package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
)

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
