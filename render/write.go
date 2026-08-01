package render

import (
	"fmt"

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

// Write renders a statement that changes database rows.
func Write(d dialect.Dialect, statement query.WriteStatement) (Statement, error) {
	switch statement := statement.(type) {
	case query.Insert:
		return Insert(d, statement)
	case query.Update:
		return Update(d, statement)
	case query.Delete:
		return Delete(d, statement)
	default:
		if statement == nil {
			return Statement{}, &Error{Err: fmt.Errorf("write statement must not be nil")}
		}
		return Statement{}, &Error{Err: fmt.Errorf("unsupported write statement %T", statement)}
	}
}

func (r *renderer) writeInsert(statement query.Insert) error {
	table, err := r.quoteIdentifier(statement.Into().Name())
	if err != nil {
		return err
	}
	r.builder.WriteString("INSERT INTO ")
	r.builder.WriteString(table)
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
	return r.writeReturning(statement.Returning())
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
