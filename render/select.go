// Package render converts validated query models into parameterized SQL.
package render

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
)

// Statement is parameterized SQL ready for execution.
type Statement struct {
	sql  string
	args []any
}

// SQL returns the rendered SQL text.
func (s Statement) SQL() string {
	return s.sql
}

// Args returns a copy of the bound arguments in placeholder order.
func (s Statement) Args() []any {
	return append([]any(nil), s.args...)
}

// Error describes a failure while rendering SQL.
type Error struct {
	Dialect string
	Err     error
}

func (e *Error) Error() string {
	if e.Dialect == "" {
		return fmt.Sprintf("render: %s", e.Err)
	}
	return fmt.Sprintf("render %s: %s", e.Dialect, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Select renders statement for d.
func Select(d dialect.Dialect, statement query.Select) (Statement, error) {
	return renderStatement(d, "SELECT", statement.Validate, func(renderer *renderer) error {
		return renderer.writeSelect(statement)
	})
}

func renderStatement(d dialect.Dialect, operation string, validate func() error, write func(*renderer) error) (Statement, error) {
	if isNilDialect(d) {
		return Statement{}, &Error{Err: fmt.Errorf("dialect must not be nil")}
	}
	if err := validate(); err != nil {
		return Statement{}, &Error{Dialect: d.Name(), Err: fmt.Errorf("invalid %s statement: %w", operation, err)}
	}
	renderer := renderer{dialect: d}
	if err := write(&renderer); err != nil {
		return Statement{}, &Error{Dialect: d.Name(), Err: err}
	}
	return Statement{sql: renderer.builder.String(), args: renderer.args}, nil
}

type renderer struct {
	dialect dialect.Dialect
	builder strings.Builder
	args    []any
}

func (r *renderer) writeSelect(statement query.Select) error {
	r.builder.WriteString("SELECT ")
	for i, projection := range statement.Projections() {
		if i > 0 {
			r.builder.WriteString(", ")
		}
		if err := r.writeProjection(projection); err != nil {
			return err
		}
	}

	r.builder.WriteString(" FROM ")
	if err := r.writeTable(statement.From()); err != nil {
		return err
	}
	for _, join := range statement.Joins() {
		r.builder.WriteByte(' ')
		r.builder.WriteString(string(join.Type()))
		r.builder.WriteString(" JOIN ")
		if err := r.writeTable(join.Source()); err != nil {
			return err
		}
		r.builder.WriteString(" ON ")
		if err := r.writeExpression(join.On()); err != nil {
			return err
		}
	}
	if where := statement.Where(); where != nil {
		r.builder.WriteString(" WHERE ")
		if err := r.writeExpression(where); err != nil {
			return err
		}
	}

	groupBy := statement.GroupBy()
	if len(groupBy) > 0 {
		r.builder.WriteString(" GROUP BY ")
		for i, expression := range groupBy {
			if i > 0 {
				r.builder.WriteString(", ")
			}
			if err := r.writeExpression(expression); err != nil {
				return err
			}
		}
	}
	if having := statement.Having(); having != nil {
		r.builder.WriteString(" HAVING ")
		if err := r.writeExpression(having); err != nil {
			return err
		}
	}

	orders := statement.OrderBy()
	if len(orders) > 0 {
		r.builder.WriteString(" ORDER BY ")
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
	if limit, ok := statement.Limit(); ok {
		r.builder.WriteString(" LIMIT ")
		if err := r.writeArgument(limit); err != nil {
			return err
		}
	}
	if offset, ok := statement.Offset(); ok {
		r.builder.WriteString(" OFFSET ")
		if err := r.writeArgument(offset); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) writeTable(table query.Table) error {
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
	if err := r.writeExpression(projection.Expression()); err != nil {
		return err
	}
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

func (r *renderer) writeExpression(expression query.Expression) error {
	switch expression := expression.(type) {
	case query.Column:
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
	case query.Value:
		return r.writeArgument(expression.Argument())
	case query.Function:
		r.builder.WriteString(string(expression.Name()))
		r.builder.WriteByte('(')
		if expression.Star() {
			r.builder.WriteByte('*')
		} else {
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
	default:
		return fmt.Errorf("unsupported expression %T", expression)
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
	return value.Kind() == reflect.Ptr && value.IsNil()
}
