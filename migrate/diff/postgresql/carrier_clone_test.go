package postgresql

import (
	"testing"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
	"github.com/stretchr/testify/require"
)

func TestCloneExpressionVariantsOwnNestedValues(t *testing.T) {
	expressions := []pgquery.Expression{
		&pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "users"}, {Name: "id"}}},
		&pgquery.StarExpression{Qualifier: pgquery.QualifiedName{{Name: "users"}}},
		&pgquery.Literal{Kind: pgquery.StringLiteral, Value: "value"},
		&pgquery.Parameter{Index: 1},
		&pgquery.UnaryExpression{Operator: "-", Expression: &pgquery.Literal{Kind: pgquery.NumberLiteral, Value: "1"}},
		&pgquery.BinaryExpression{Left: &pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "a"}}}, Operator: "+", Right: &pgquery.Parameter{Index: 2}},
		&pgquery.CallExpression{Function: pgquery.QualifiedName{{Name: "coalesce"}}, Arguments: []pgquery.Expression{&pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "a"}}}, &pgquery.Literal{Value: "fallback"}}},
	}
	for _, expression := range expressions {
		cloned, err := cloneExpression(expression)
		require.NoError(t, err)
		require.Equal(t, expression, cloned)
		require.NotSame(t, expression, cloned)
	}

	original := expressions[0].(*pgquery.IdentifierExpression)
	cloned := mustExpressionClone(t, original).(*pgquery.IdentifierExpression)
	original.Name[0].Name = "changed"
	require.Equal(t, "users", cloned.Name[0].Name)
}

func TestCloneRecursiveExpressionChildrenOwnEveryLevel(t *testing.T) {
	original := &pgquery.CallExpression{
		Function: pgquery.QualifiedName{{Name: "coalesce"}},
		Arguments: []pgquery.Expression{
			&pgquery.UnaryExpression{Operator: "-", Expression: &pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "value"}}}},
			&pgquery.BinaryExpression{
				Left:     &pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "left"}}},
				Operator: "+",
				Right:    &pgquery.CallExpression{Function: pgquery.QualifiedName{{Name: "lower"}}, Arguments: []pgquery.Expression{&pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "right"}}}}},
			},
		},
	}
	cloned, err := cloneExpression(original)
	require.NoError(t, err)

	original.Function[0].Name = "changed_function"
	original.Arguments[0].(*pgquery.UnaryExpression).Expression.(*pgquery.IdentifierExpression).Name[0].Name = "changed_unary"
	binary := original.Arguments[1].(*pgquery.BinaryExpression)
	binary.Left.(*pgquery.IdentifierExpression).Name[0].Name = "changed_left"
	binary.Right.(*pgquery.CallExpression).Function[0].Name = "changed_nested_function"
	binary.Right.(*pgquery.CallExpression).Arguments[0].(*pgquery.IdentifierExpression).Name[0].Name = "changed_right"

	copy := cloned.(*pgquery.CallExpression)
	require.Equal(t, "coalesce", copy.Function[0].Name)
	require.Equal(t, "value", copy.Arguments[0].(*pgquery.UnaryExpression).Expression.(*pgquery.IdentifierExpression).Name[0].Name)
	require.Equal(t, "left", copy.Arguments[1].(*pgquery.BinaryExpression).Left.(*pgquery.IdentifierExpression).Name[0].Name)
	require.Equal(t, "lower", copy.Arguments[1].(*pgquery.BinaryExpression).Right.(*pgquery.CallExpression).Function[0].Name)
	require.Equal(t, "right", copy.Arguments[1].(*pgquery.BinaryExpression).Right.(*pgquery.CallExpression).Arguments[0].(*pgquery.IdentifierExpression).Name[0].Name)
}

func TestCloneExpressionSlicesPreserveNilAndEmpty(t *testing.T) {
	cloned, err := cloneExpressions(nil)
	require.NoError(t, err)
	require.Nil(t, cloned)
	cloned, err = cloneExpressions([]pgquery.Expression{})
	require.NoError(t, err)
	require.NotNil(t, cloned)
	require.Empty(t, cloned)
}

func TestCloneIndexOwnsElementsAndPredicate(t *testing.T) {
	statement := &pgquery.CreateIndexStatement{
		Name:   ptrQualifiedName(pgquery.QualifiedName{{Name: "users_name"}}),
		Table:  pgquery.QualifiedName{{Name: "users"}},
		Method: ptrIdentifier(pgquery.Identifier{Name: "btree"}),
		Elements: []pgquery.IndexElement{{
			Expression: &pgquery.CallExpression{Function: pgquery.QualifiedName{{Name: "lower"}}, Arguments: []pgquery.Expression{&pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "name"}}}}},
		}},
		Where: &pgquery.BinaryExpression{Left: &pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "active"}}}, Operator: "=", Right: &pgquery.Literal{Value: "true"}},
	}
	cloned, err := cloneIndex(statement)
	require.NoError(t, err)
	require.Equal(t, statement, cloned)
	statement.Elements[0].Expression.(*pgquery.CallExpression).Function[0].Name = "changed"
	statement.Where.(*pgquery.BinaryExpression).Left.(*pgquery.IdentifierExpression).Name[0].Name = "changed"
	(*statement.Name)[0].Name = "changed"
	statement.Table[0].Name = "changed"
	statement.Method.Name = "changed"
	require.Equal(t, "lower", cloned.Elements[0].Expression.(*pgquery.CallExpression).Function[0].Name)
	require.Equal(t, "active", cloned.Where.(*pgquery.BinaryExpression).Left.(*pgquery.IdentifierExpression).Name[0].Name)
	require.Equal(t, "users_name", (*cloned.Name)[0].Name)
	require.Equal(t, "users", cloned.Table[0].Name)
	require.Equal(t, "btree", cloned.Method.Name)
}

func TestCloneTableContainersPreserveNilAndAllocatedEmpty(t *testing.T) {
	var nilTable *pgquery.CreateTableStatement
	cloned, err := cloneCreateTableStatement(nilTable)
	require.NoError(t, err)
	require.Nil(t, cloned)

	nilStatement := &pgquery.CreateTableStatement{}
	cloned, err = cloneCreateTableStatement(nilStatement)
	require.NoError(t, err)
	require.Nil(t, cloned.Name)
	require.Nil(t, cloned.Columns)
	require.Nil(t, cloned.Constraints)

	emptyStatement := &pgquery.CreateTableStatement{
		Name:        pgquery.QualifiedName{},
		Columns:     []pgquery.ColumnDefinition{},
		Constraints: []pgquery.TableConstraint{},
	}
	cloned, err = cloneCreateTableStatement(emptyStatement)
	require.NoError(t, err)
	require.NotNil(t, cloned.Name)
	require.NotNil(t, cloned.Columns)
	require.NotNil(t, cloned.Constraints)
	require.NotSame(t, &emptyStatement.Columns, &cloned.Columns)
	require.NotSame(t, &emptyStatement.Constraints, &cloned.Constraints)

	column, err := cloneColumn(pgquery.ColumnDefinition{})
	require.NoError(t, err)
	require.Nil(t, column.Type.Words)
	require.Nil(t, column.Type.Modifiers)
	require.Nil(t, column.Constraints)
	column, err = cloneColumn(pgquery.ColumnDefinition{Type: pgquery.DataType{Words: []string{}, Modifiers: []pgquery.Expression{}}, Constraints: []pgquery.ColumnConstraint{}})
	require.NoError(t, err)
	require.NotNil(t, column.Type.Words)
	require.NotNil(t, column.Type.Modifiers)
	require.NotNil(t, column.Constraints)

	constraint, err := cloneTableConstraint(pgquery.TableConstraint{})
	require.NoError(t, err)
	require.Nil(t, constraint.Columns)
	constraint, err = cloneTableConstraint(pgquery.TableConstraint{Columns: []pgquery.Identifier{}})
	require.NoError(t, err)
	require.NotNil(t, constraint.Columns)
	reference := cloneReference(&pgquery.Reference{})
	require.Nil(t, reference.Table)
	require.Nil(t, reference.Columns)
	reference = cloneReference(&pgquery.Reference{Table: pgquery.QualifiedName{}, Columns: []pgquery.Identifier{}})
	require.NotNil(t, reference.Table)
	require.NotNil(t, reference.Columns)
}

func TestCloneTableNestedIdentifiersAndReferencesOwnValues(t *testing.T) {
	original := &pgquery.CreateTableStatement{
		Name: pgquery.QualifiedName{{Name: "public"}, {Name: "users"}},
		Columns: []pgquery.ColumnDefinition{{
			Name: pgquery.Identifier{Name: "role_id"},
			Type: pgquery.DataType{Words: []string{"int"}, Modifiers: []pgquery.Expression{&pgquery.Literal{Value: "4"}}},
			Constraints: []pgquery.ColumnConstraint{{
				Name:       &pgquery.Identifier{Name: "role_fk"},
				Expression: &pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "role_id"}}},
				References: &pgquery.Reference{Table: pgquery.QualifiedName{{Name: "roles"}}, Columns: []pgquery.Identifier{{Name: "id"}}},
			}},
		}},
		Constraints: []pgquery.TableConstraint{{
			Name:       &pgquery.Identifier{Name: "users_pk"},
			Columns:    []pgquery.Identifier{{Name: "role_id"}},
			Expression: &pgquery.BinaryExpression{Left: &pgquery.IdentifierExpression{Name: pgquery.QualifiedName{{Name: "role_id"}}}, Operator: "=", Right: &pgquery.Literal{Value: "1"}},
			References: &pgquery.Reference{Table: pgquery.QualifiedName{{Name: "roles"}}, Columns: []pgquery.Identifier{{Name: "id"}}},
		}},
	}
	cloned, err := cloneCreateTableStatement(original)
	require.NoError(t, err)

	original.Name[0].Name = "changed"
	original.Columns[0].Name.Name = "changed"
	original.Columns[0].Type.Words[0] = "text"
	original.Columns[0].Type.Modifiers[0].(*pgquery.Literal).Value = "8"
	original.Columns[0].Constraints[0].Name.Name = "changed"
	original.Columns[0].Constraints[0].Expression.(*pgquery.IdentifierExpression).Name[0].Name = "changed"
	original.Columns[0].Constraints[0].References.Table[0].Name = "changed"
	original.Columns[0].Constraints[0].References.Columns[0].Name = "changed"
	original.Constraints[0].Name.Name = "changed"
	original.Constraints[0].Columns[0].Name = "changed"
	original.Constraints[0].Expression.(*pgquery.BinaryExpression).Left.(*pgquery.IdentifierExpression).Name[0].Name = "changed"
	original.Constraints[0].References.Table[0].Name = "changed"
	original.Constraints[0].References.Columns[0].Name = "changed"

	require.Equal(t, "public", cloned.Name[0].Name)
	require.Equal(t, "role_id", cloned.Columns[0].Name.Name)
	require.Equal(t, "int", cloned.Columns[0].Type.Words[0])
	require.Equal(t, "4", cloned.Columns[0].Type.Modifiers[0].(*pgquery.Literal).Value)
	require.Equal(t, "role_fk", cloned.Columns[0].Constraints[0].Name.Name)
	require.Equal(t, "role_id", cloned.Columns[0].Constraints[0].Expression.(*pgquery.IdentifierExpression).Name[0].Name)
	require.Equal(t, "roles", cloned.Columns[0].Constraints[0].References.Table[0].Name)
	require.Equal(t, "id", cloned.Columns[0].Constraints[0].References.Columns[0].Name)
	require.Equal(t, "users_pk", cloned.Constraints[0].Name.Name)
	require.Equal(t, "role_id", cloned.Constraints[0].Columns[0].Name)
	require.Equal(t, "role_id", cloned.Constraints[0].Expression.(*pgquery.BinaryExpression).Left.(*pgquery.IdentifierExpression).Name[0].Name)
	require.Equal(t, "roles", cloned.Constraints[0].References.Table[0].Name)
	require.Equal(t, "id", cloned.Constraints[0].References.Columns[0].Name)
}

func TestCloneIdentifierAndFactMapsOwnContainers(t *testing.T) {
	var nilName pgquery.QualifiedName
	require.Nil(t, cloneQualifiedName(nilName))
	emptyName := pgquery.QualifiedName{}
	clonedName := cloneQualifiedName(emptyName)
	require.NotNil(t, clonedName)

	identities, err := cloneTableDefinition(tableDefinition{identities: nil, foreignKeys: nil})
	require.NoError(t, err)
	require.Nil(t, identities.identities)
	require.Nil(t, identities.foreignKeys)
	emptyIdentities := map[string]identityMode{}
	emptyForeignKeys := map[foreignKeyKey]foreignKeyActions{}
	cloned, err := cloneTableDefinition(tableDefinition{identities: emptyIdentities, foreignKeys: emptyForeignKeys})
	require.NoError(t, err)
	require.NotNil(t, cloned.identities)
	require.NotNil(t, cloned.foreignKeys)
	identityMap := map[string]identityMode{"2:id": identityAlways}
	foreignKeyMap := map[foreignKeyKey]foreignKeyActions{{table: "1:t", constraint: "1:c"}: {onDelete: referenceCascade}}
	cloned, err = cloneTableDefinition(tableDefinition{identities: identityMap, foreignKeys: foreignKeyMap})
	require.NoError(t, err)
	identityMap["2:id"] = identityByDefault
	foreignKeyMap[foreignKeyKey{table: "1:t", constraint: "1:c"}] = foreignKeyActions{onDelete: referenceRestrict}
	require.Equal(t, identityAlways, cloned.identities["2:id"])
	require.Equal(t, referenceCascade, cloned.foreignKeys[foreignKeyKey{table: "1:t", constraint: "1:c"}].onDelete)
}

func TestCloneIndexContainersPreserveNilAndAllocatedEmpty(t *testing.T) {
	cloned, err := cloneIndex(nil)
	require.NoError(t, err)
	require.Nil(t, cloned)
	statement := &pgquery.CreateIndexStatement{}
	cloned, err = cloneIndex(statement)
	require.NoError(t, err)
	require.Nil(t, cloned.Elements)
	statement.Elements = []pgquery.IndexElement{}
	cloned, err = cloneIndex(statement)
	require.NoError(t, err)
	require.NotNil(t, cloned.Elements)
	statement.Name = ptrQualifiedName(pgquery.QualifiedName{})
	statement.Table = pgquery.QualifiedName{}
	cloned, err = cloneIndex(statement)
	require.NoError(t, err)
	require.NotNil(t, cloned.Name)
	require.NotNil(t, cloned.Table)
}

func mustExpressionClone(t *testing.T, expression pgquery.Expression) pgquery.Expression {
	cloned, err := cloneExpression(expression)
	require.NoError(t, err)
	return cloned
}

func ptrQualifiedName(name pgquery.QualifiedName) *pgquery.QualifiedName {
	return &name
}

func ptrIdentifier(value pgquery.Identifier) *pgquery.Identifier {
	return &value
}
