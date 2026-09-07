package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

func TestMutationVersionedReturningCardinalityMatrix(t *testing.T) {
	for _, test := range []struct {
		name string
		rows int
	}{
		{name: "zero", rows: 0}, {name: "one", rows: 1}, {name: "two", rows: 2},
	} {
		for _, terminal := range []string{"Rows", "All", "One", "Maybe"} {
			t.Run(test.name+"/"+terminal, func(t *testing.T) {
				executor, returned := newVersionedReturningAcceptance(t, test.rows)
				switch terminal {
				case "Rows":
					sequence, err := rasql.Rows(t.Context(), executor, returned)
					require.NoError(t, err)
					seen := 0
					var terminalErr error
					for item, itemErr := range sequence {
						if itemErr != nil {
							terminalErr = itemErr
							break
						}
						seen++
						_ = item
						if test.rows == 1 {
							break
						}
					}
					expectedSeen := 0
					if test.rows == 1 {
						expectedSeen = 1
					}
					require.Equal(t, expectedSeen, seen)
					if test.rows == 0 {
						require.ErrorIs(t, terminalErr, rasql.ErrPrecondition)
					} else if test.rows == 2 {
						require.ErrorIs(t, terminalErr, rasql.ErrMultipleRows)
					} else {
						require.NoError(t, terminalErr)
					}
				case "All":
					values, err := rasql.All(t.Context(), executor, returned)
					if test.rows == 0 {
						require.ErrorIs(t, err, rasql.ErrPrecondition)
						require.Empty(t, values)
					} else if test.rows == 2 {
						require.ErrorIs(t, err, rasql.ErrMultipleRows)
						require.Empty(t, values)
					} else {
						require.NoError(t, err)
						require.Len(t, values, 1)
					}
				case "One":
					value, err := rasql.One(t.Context(), executor, returned)
					if test.rows == 0 {
						require.ErrorIs(t, err, rasql.ErrPrecondition)
					} else if test.rows == 2 {
						require.ErrorIs(t, err, rasql.ErrMultipleRows)
					} else {
						require.NoError(t, err)
						require.Equal(t, int64(1), value.ID)
					}
				case "Maybe":
					value, ok, err := rasql.Maybe(t.Context(), executor, returned)
					if test.rows == 0 {
						require.ErrorIs(t, err, rasql.ErrPrecondition)
						require.False(t, ok)
					} else if test.rows == 2 {
						require.ErrorIs(t, err, rasql.ErrMultipleRows)
						require.False(t, ok)
					} else {
						require.NoError(t, err)
						require.True(t, ok)
						require.Equal(t, int64(1), value.ID)
					}
				}
			})
		}
	}
}

func newVersionedReturningAcceptance(t *testing.T, rows int) (rasql.Executor, rasql.Query[mutationRow]) {
	t.Helper()
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	relation, err := rasql.SourceOf[mutationRow](table, "")
	require.NoError(t, err)
	version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
	require.NoError(t, err)
	for i := int64(1); i <= 2; i++ {
		create, createErr := rasql.NewCreatePlan(table, rasql.SetField(id, i), rasql.SetField(value, "before"))
		require.NoError(t, createErr)
		_, createErr = rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, createErr)
	}
	where := query.EqualValue(id, int64(99))
	if rows == 1 {
		where = query.EqualValue(id, int64(1))
	} else if rows == 2 {
		where = query.GreaterOrEqualValue(id, int64(1))
	}
	patch, err := rasql.NewPatchPlan(table, where, rasql.SetField(value, "after"))
	require.NoError(t, err)
	patch, err = patch.WithVersion(version, 1)
	require.NoError(t, err)
	returned, err := rasql.Returning(patch, mutationProjection(t, table))
	require.NoError(t, err)
	return executor, returned
}

func TestMutationVersionedPatchCanonicalizesAliasedVersionColumn(t *testing.T) {
	executor, table, id := mutationFixture(t)
	value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
	relation, err := rasql.SourceOf[mutationRow](table, "vsrc")
	require.NoError(t, err)
	version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
	require.NoError(t, err)
	create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(10)), rasql.SetField(value, "before"))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, create)
	require.NoError(t, err)
	patch, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(10)), rasql.SetField(value, "after"))
	require.NoError(t, err)
	patch, err = patch.WithVersion(version, 1)
	require.NoError(t, err)
	outcome, err := rasql.ExecMutation(t.Context(), executor, patch)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
}
