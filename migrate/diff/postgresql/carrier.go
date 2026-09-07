package postgresql

import (
	"fmt"
	"reflect"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
)

func cloneTableDefinition(table tableDefinition) (tableDefinition, error) {
	statement, err := cloneCreateTableStatement(table.statement)
	if err != nil {
		return tableDefinition{}, err
	}
	return tableDefinition{source: table.source, statement: statement, identities: cloneIdentityModes(table.identities), foreignKeys: cloneForeignKeyActions(table.foreignKeys)}, nil
}
func cloneCreateTableStatement(in *pgquery.CreateTableStatement) (*pgquery.CreateTableStatement, error) {
	if in == nil {
		return nil, nil
	}
	out := &pgquery.CreateTableStatement{Persistence: in.Persistence, IfNotExists: in.IfNotExists, Name: cloneQualifiedName(in.Name)}
	if in.Columns != nil {
		out.Columns = make([]pgquery.ColumnDefinition, len(in.Columns))
		for i := range in.Columns {
			column, err := cloneColumn(in.Columns[i])
			if err != nil {
				return nil, err
			}
			out.Columns[i] = column
		}
	}
	if in.Constraints != nil {
		out.Constraints = make([]pgquery.TableConstraint, len(in.Constraints))
		for i := range in.Constraints {
			constraint, err := cloneTableConstraint(in.Constraints[i])
			if err != nil {
				return nil, err
			}
			out.Constraints[i] = constraint
		}
	}
	return out, nil
}
func cloneColumn(in pgquery.ColumnDefinition) (pgquery.ColumnDefinition, error) {
	out := pgquery.ColumnDefinition{Name: in.Name, Type: pgquery.DataType{Words: cloneStrings(in.Type.Words), ArrayDimensions: in.Type.ArrayDimensions}}
	modifiers, err := cloneExpressions(in.Type.Modifiers)
	if err != nil {
		return pgquery.ColumnDefinition{}, err
	}
	out.Type.Modifiers = modifiers
	if in.Constraints != nil {
		out.Constraints = make([]pgquery.ColumnConstraint, len(in.Constraints))
		for i, c := range in.Constraints {
			expression, err := cloneExpression(c.Expression)
			if err != nil {
				return pgquery.ColumnDefinition{}, err
			}
			out.Constraints[i] = pgquery.ColumnConstraint{Name: cloneIdentifierPtr(c.Name), Kind: c.Kind, Expression: expression, References: cloneReference(c.References)}
		}
	}
	return out, nil
}
func cloneTableConstraint(in pgquery.TableConstraint) (pgquery.TableConstraint, error) {
	expression, err := cloneExpression(in.Expression)
	if err != nil {
		return pgquery.TableConstraint{}, err
	}
	return pgquery.TableConstraint{Name: cloneIdentifierPtr(in.Name), Kind: in.Kind, Columns: cloneIdentifiers(in.Columns), Expression: expression, References: cloneReference(in.References)}, nil
}
func cloneReference(in *pgquery.Reference) *pgquery.Reference {
	if in == nil {
		return nil
	}
	return &pgquery.Reference{Table: cloneQualifiedName(in.Table), Columns: cloneIdentifiers(in.Columns)}
}
func cloneQualifiedName(in pgquery.QualifiedName) pgquery.QualifiedName {
	if in == nil {
		return nil
	}
	out := make(pgquery.QualifiedName, len(in))
	copy(out, in)
	return out
}
func cloneIdentifiers(in []pgquery.Identifier) []pgquery.Identifier {
	if in == nil {
		return nil
	}
	out := make([]pgquery.Identifier, len(in))
	copy(out, in)
	return out
}
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
func cloneIdentifierPtr(in *pgquery.Identifier) *pgquery.Identifier {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
func cloneExpressions(in []pgquery.Expression) ([]pgquery.Expression, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]pgquery.Expression, len(in))
	for i := range in {
		cloned, err := cloneExpression(in[i])
		if err != nil {
			return nil, err
		}
		out[i] = cloned
	}
	return out, nil
}
func cloneExpression(in pgquery.Expression) (pgquery.Expression, error) {
	switch e := in.(type) {
	case nil:
		return nil, nil
	case *pgquery.IdentifierExpression:
		return &pgquery.IdentifierExpression{Name: cloneQualifiedName(e.Name)}, nil
	case *pgquery.StarExpression:
		return &pgquery.StarExpression{Qualifier: cloneQualifiedName(e.Qualifier)}, nil
	case *pgquery.Literal:
		out := *e
		return &out, nil
	case *pgquery.Parameter:
		out := *e
		return &out, nil
	case *pgquery.UnaryExpression:
		expression, err := cloneExpression(e.Expression)
		if err != nil {
			return nil, err
		}
		return &pgquery.UnaryExpression{Operator: e.Operator, Expression: expression}, nil
	case *pgquery.BinaryExpression:
		left, err := cloneExpression(e.Left)
		if err != nil {
			return nil, err
		}
		right, err := cloneExpression(e.Right)
		if err != nil {
			return nil, err
		}
		return &pgquery.BinaryExpression{Left: left, Operator: e.Operator, Right: right}, nil
	case *pgquery.CallExpression:
		arguments, err := cloneExpressions(e.Arguments)
		if err != nil {
			return nil, err
		}
		return &pgquery.CallExpression{Function: cloneQualifiedName(e.Function), Arguments: arguments}, nil
	default:
		return nil, fmt.Errorf("postgresql schema diff: unsupported expression clone type %T", in)
	}
}
func cloneColumnPointer(in *pgquery.ColumnDefinition) (*pgquery.ColumnDefinition, error) {
	if in == nil {
		return nil, nil
	}
	out, err := cloneColumn(*in)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func cloneIndex(in *pgquery.CreateIndexStatement) (*pgquery.CreateIndexStatement, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	out.Name = nil
	if in.Name != nil {
		n := cloneQualifiedName(*in.Name)
		out.Name = &n
	}
	out.Table = cloneQualifiedName(in.Table)
	if in.Method != nil {
		m := *in.Method
		out.Method = &m
	}
	if in.Elements != nil {
		out.Elements = make([]pgquery.IndexElement, len(in.Elements))
		for i, e := range in.Elements {
			expression, err := cloneExpression(e.Expression)
			if err != nil {
				return nil, err
			}
			out.Elements[i] = pgquery.IndexElement{Direction: e.Direction, Expression: expression}
		}
	}
	where, err := cloneExpression(in.Where)
	if err != nil {
		return nil, err
	}
	out.Where = where
	return &out, nil
}
func equalIdentityFacts(left, right map[string]identityMode) bool {
	return reflect.DeepEqual(left, right)
}
func equalForeignKeyActions(left, right map[foreignKeyKey]foreignKeyActions) bool {
	return reflect.DeepEqual(left, right)
}
