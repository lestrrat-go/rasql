package query

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestFunctionValidationRejectsStarOnNonCountFunctions covers the star branch
// of the Function case in validateExpression. No exported constructor can
// build this value: only CountAll sets star, and it always pairs star with
// FunctionCount. That makes this the "no other way around it" case for an
// in-package test, unlike the rest of the Function suite in select_test.go.
func TestFunctionValidationRejectsStarOnNonCountFunctions(t *testing.T) {
	_, err := validateExpression(Function{name: FunctionSum, star: true}, projectionContext(sourceScope{}), "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")
}

// TestFunctionValidationRejectsStarOnScalarFunctions covers the same star
// branch for a curated scalar name and for the Func escape hatch. No exported
// constructor can build either value: Call takes no star flag, and Func's
// arguments always populate arguments rather than star.
func TestFunctionValidationRejectsStarOnScalarFunctions(t *testing.T) {
	_, err := validateExpression(Function{name: FunctionLower, star: true}, projectionContext(sourceScope{}), "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")

	_, err = validateExpression(Function{name: "jsonb_path_query", star: true, unchecked: true}, projectionContext(sourceScope{}), "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")
}

// TestFunctionValidationMessageNamesHavingAndGroupedOrderBy pins the rewritten
// misplaced-aggregate message so a future edit cannot drop the HAVING mention
// or narrow the ORDER BY wording back to "whose projections all aggregate" now
// that a statement can group explicitly.
func TestFunctionValidationMessageNamesHavingAndGroupedOrderBy(t *testing.T) {
	_, err := validateExpression(Call(FunctionCount, Value{}), clauseContext(sourceScope{}, "a WHERE clause"), "where")
	require.Error(t, err)
	require.ErrorContains(t, err, "is only valid in a SELECT projection, in a HAVING clause, or in the ORDER BY clause of a statement that groups")
}

// TestEveryBuilderMarksTheStatementChecked is what makes the narrowed builders
// safe. WithLimit, WithOffset, WithLock, and WithDistinct trust the checked
// field to mean their receiver already passed Validate, so a constructor or
// With... method that returned a statement without setting it would hand them
// an unvalidated receiver they would then wave through. The field is
// unexported and only this package can read it, which is why this test is not
// in the external suite.
func TestEveryBuilderMarksTheStatementChecked(t *testing.T) {
	users, err := NewTableRef(usersTableForCheckedTest())
	require.NoError(t, err)
	orders, err := NewTableRef(ordersTableForCheckedTest())
	require.NoError(t, err)
	id := users.Column("id")
	base, err := NewSelect(users, Project(id))
	require.NoError(t, err)
	require.True(t, base.checked, "NewSelect")

	tests := []struct {
		name  string
		build func() (Select, error)
	}{
		{"NewCorrelatedSelect", func() (Select, error) { return NewCorrelatedSelect(users, nil, Project(id)) }},
		{"NewGroupedSelect", func() (Select, error) { return NewGroupedSelect(users, []Expression{id}, Project(id)) }},
		{"NewJoinedSelect", func() (Select, error) { return NewJoinedSelect(users, nil, nil, Project(id)) }},
		{"NewCorrelatedJoinedSelect", func() (Select, error) {
			return NewCorrelatedJoinedSelect(users, nil, nil, nil, Project(id))
		}},
		{"WithCorrelation", func() (Select, error) { return base.WithCorrelation(orders) }},
		{"WithJoin", func() (Select, error) {
			return base.WithJoin(InnerJoin(orders, Equal(id, orders.Column("user_id"))))
		}},
		{"WithWhere", func() (Select, error) { return base.WithWhere(Equal(id, Bind(int64(1)))) }},
		{"WithGroupBy", func() (Select, error) { return base.WithGroupBy(id) }},
		{"WithDistinct", func() (Select, error) { return base.WithDistinct() }},
		{"WithOrder", func() (Select, error) { return base.WithOrder(Asc(id)) }},
		{"WithLimit", func() (Select, error) { return base.WithLimit(10) }},
		{"WithOffset", func() (Select, error) { return base.WithOffset(10) }},
		{"WithLock", func() (Select, error) { return base.WithLock(RowLock(LockUpdate)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statement, err := test.build()
			require.NoError(t, err)
			require.True(t, statement.checked, "%s returned a statement it did not mark checked", test.name)
		})
	}
}

func usersTableForCheckedTest() schema.TableDef {
	return schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
}

func ordersTableForCheckedTest() schema.TableDef {
	return schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

// TestValidationClearsTheMarkBeforeJudgingAChangedStatement guards the one way
// this design fails open. clone copies the checked mark, so a value that is
// changed and then validated would be reported valid on the strength of what
// the statement said before the change. validated() and
// ValidateCompilerExpression both clear the mark first; dropping either clear
// makes one of these subtests report nil where it wants an error.
func TestValidationClearsTheMarkBeforeJudgingAChangedStatement(t *testing.T) {
	users, err := NewTableRef(usersTableForCheckedTest())
	require.NoError(t, err)
	id := users.Column("id")
	base, err := NewSelect(users, Project(id))
	require.NoError(t, err)
	require.True(t, base.checked, "the statement under test has to carry the mark")

	// WithWhere goes through validated(). An aggregate is not allowed in a
	// WHERE clause, so the walk has to run and refuse it rather than report
	// the statement base already passed.
	t.Run("validated", func(t *testing.T) {
		_, err := base.WithWhere(GreaterThan(Count(id), Bind(int64(1))))
		require.Error(t, err)
		require.ErrorContains(t, err, "where")
	})

	// ValidateCompilerExpression substitutes an expression into a clone and
	// validates that, so it has to judge the substituted expression.
	t.Run("ValidateCompilerExpression", func(t *testing.T) {
		err := base.ValidateCompilerExpression(Count(id), "where", 0)
		require.Error(t, err)
		require.ErrorContains(t, err, "where")
	})
}
