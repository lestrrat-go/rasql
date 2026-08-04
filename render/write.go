package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
)

// Insert renders statement for d.
func Insert(d dialect.Dialect, statement query.Insert) (Statement, error) {
	return renderStatement(d, "INSERT", statement.Validate, func(renderer *renderer) error {
		return renderer.writeInsert(statement)
	})
}

// Update renders statement for d.
func Update(d dialect.Dialect, statement query.Update) (Statement, error) {
	return renderStatement(d, "UPDATE", statement.Validate, func(renderer *renderer) error {
		return renderer.writeUpdate(statement)
	})
}

// Delete renders statement for d.
func Delete(d dialect.Dialect, statement query.Delete) (Statement, error) {
	return renderStatement(d, "DELETE", statement.Validate, func(renderer *renderer) error {
		return renderer.writeDelete(statement)
	})
}

// Upsert renders statement for d.
func Upsert(d dialect.Dialect, statement query.Upsert) (Statement, error) {
	return renderStatement(d, "UPSERT", statement.Validate, func(renderer *renderer) error {
		return renderer.writeUpsert(statement)
	})
}

// Write renders a statement that changes database rows.
func Write(d dialect.Dialect, statement query.WriteStatement) (Statement, error) {
	switch statement := statement.(type) {
	case query.Insert:
		return Insert(d, statement)
	case query.Update:
		return Update(d, statement)
	case query.Delete:
		return Delete(d, statement)
	case query.Upsert:
		return Upsert(d, statement)
	default:
		if statement == nil {
			return Statement{}, &Error{Err: fmt.Errorf("write statement must not be nil")}
		}
		return Statement{}, &Error{Err: fmt.Errorf("unsupported write statement %T", statement)}
	}
}

func (r *renderer) writeInsert(statement query.Insert) error {
	if err := r.writeInsertBase(statement); err != nil {
		return err
	}
	return r.writeReturning(statement.Returning())
}

func (r *renderer) writeInsertBase(statement query.Insert) error {
	table, err := r.quoteIdentifier(statement.Into().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("INSERT INTO ")
	r.builder.WriteString(table)
	if statement.UsesDefaultValues() {
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
	for i, column := range statement.Columns() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		name, err := r.quoteIdentifier(column.Name())
		if err != nil {
			return err
		}
		r.builder.WriteString(name)
	}
	r.builder.WriteString(") VALUES (")
	for i, value := range statement.Values() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		if err := r.writeExpression(value); err != nil {
			return err
		}
	}
	r.builder.WriteByte(')')
	return nil
}

// Each upsertMessage constant below is the only definition of its error text.
// The conflict-target check in writeUpsert joins its own message with the other
// problems that apply to the same statement, so none of these texts may be
// copied anywhere else.
const (
	upsertConflictTargetMessage = "explicit conflict target is not supported: this dialect lacks dialect.CapabilityConflictTarget"
	upsertDefaultValuesMessage  = "default-values upsert is not supported"
	upsertNoAssignmentsMessage  = "upsert without assignments is not supported"
)

func (r *renderer) writeUpsert(statement query.Upsert) error {
	style := r.dialect.UpsertStyle()
	if !r.dialect.Supports(dialect.CapabilityUpsert) || style == dialect.UpsertUnsupported {
		return fmt.Errorf("upsert is not supported")
	}
	defaultValuesRejected := statement.Insert().UsesDefaultValues() && !r.dialect.Supports(dialect.CapabilityDefaultValuesUpsert)
	if len(statement.ConflictColumns()) > 0 && !r.dialect.Supports(dialect.CapabilityConflictTarget) {
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
		if style == dialect.UpsertDuplicateKey && len(statement.Assignments()) == 0 {
			problems = append(problems, upsertNoAssignmentsMessage)
		}
		return errors.New(strings.Join(problems, "; "))
	}
	if defaultValuesRejected {
		return errors.New(upsertDefaultValuesMessage)
	}
	if err := r.writeInsertBase(statement.Insert()); err != nil {
		return err
	}
	switch style {
	case dialect.UpsertOnConflict:
		conflict := statement.ConflictColumns()
		assignments := statement.Assignments()
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
		assignments := statement.Assignments()
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
	return r.writeReturning(statement.Returning())
}

func (r *renderer) writeUpsertAssignments(assignments []query.Assignment, style dialect.UpsertStyle) error {
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
		if err := r.writeUpsertExpression(assignment.Value(), style); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) writeUpsertExpression(expression query.Expression, style dialect.UpsertStyle) error {
	excluded, ok := expression.(query.ExcludedColumn)
	if !ok {
		return r.writeExpression(expression)
	}
	name, err := r.quoteIdentifier(excluded.Column().Name())
	if err != nil {
		return err
	}
	switch style {
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

func (r *renderer) writeUpdate(statement query.Update) error {
	table, err := r.quoteIdentifier(statement.Table().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("UPDATE ")
	r.builder.WriteString(table)
	r.builder.WriteString(" SET ")
	for i, assignment := range statement.Assignments() {
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
	if where := statement.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}
	return r.writeReturning(statement.Returning())
}

func (r *renderer) writeDelete(statement query.Delete) error {
	table, err := r.quoteIdentifier(statement.From().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("DELETE FROM ")
	r.builder.WriteString(table)
	if where := statement.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}
	return r.writeReturning(statement.Returning())
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
	column, ok := projection.Expression().(query.Column)
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
