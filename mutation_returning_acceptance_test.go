package rasql_test

import (
	"errors"
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
		t.Run(test.name, func(t *testing.T) {
			executor, table, id := mutationFixture(t)
			value := query.TypedColumnOf[mutationRow, string](table.Column("value"))
			relation, err := rasql.SourceOf[mutationRow](table, "")
			require.NoError(t, err)
			version, err := rasql.BindColumn[mutationRow, int64](relation, "version", "")
			require.NoError(t, err)
			for i := 1; i <= 2; i++ {
				create, createErr := rasql.NewCreatePlan(table, rasql.SetField(id, int64(i)), rasql.SetField(value, "before"))
				require.NoError(t, createErr)
				_, createErr = rasql.ExecMutation(t.Context(), executor, create)
				require.NoError(t, createErr)
			}
			where := query.EqualValue(id, int64(99))
			switch test.rows {
			case 1:
				where = query.EqualValue(id, int64(1))
			case 2:
				where = query.GreaterOrEqualValue(id, int64(1))
			}
			patch, err := rasql.NewPatchPlan(table, where, rasql.SetField(value, "after"))
			require.NoError(t, err)
			patch, err = patch.WithVersion(version, 1)
			require.NoError(t, err)
			returned, err := rasql.Returning(patch, mutationProjection(t, table))
			require.NoError(t, err)

			all, allErr := rasql.All(t.Context(), executor, returned)
			switch test.rows {
			case 0:
				require.ErrorIs(t, allErr, rasql.ErrPrecondition)
				require.Empty(t, all)
			case 1:
				require.NoError(t, allErr)
				require.Len(t, all, 1)
			case 2:
				require.ErrorIs(t, allErr, rasql.ErrMultipleRows)
				require.Empty(t, all)
			}

			if test.rows == 1 {
				one, oneErr := rasql.One(t.Context(), executor, returned)
				require.NoError(t, oneErr)
				require.Equal(t, int64(1), one.ID)
				maybe, ok, maybeErr := rasql.Maybe(t.Context(), executor, returned)
				require.NoError(t, maybeErr)
				require.True(t, ok)
				require.Equal(t, int64(1), maybe.ID)
			}
			if test.rows == 0 {
				_, oneErr := rasql.One(t.Context(), executor, returned)
				require.ErrorIs(t, oneErr, rasql.ErrPrecondition)
				_, ok, maybeErr := rasql.Maybe(t.Context(), executor, returned)
				require.ErrorIs(t, maybeErr, rasql.ErrPrecondition)
				require.False(t, ok)
			}
			if test.rows == 2 {
				sequence, seqErr := rasql.Rows(t.Context(), executor, returned)
				require.NoError(t, seqErr)
				seen := 0
				var terminal error
				for item, itemErr := range sequence {
					seen++
					terminal = itemErr
					_ = item
					break
				}
				require.Zero(t, seen)
				require.True(t, errors.Is(terminal, rasql.ErrMultipleRows))
				_, oneErr := rasql.One(t.Context(), executor, returned)
				require.ErrorIs(t, oneErr, rasql.ErrMultipleRows)
				_, ok, maybeErr := rasql.Maybe(t.Context(), executor, returned)
				require.ErrorIs(t, maybeErr, rasql.ErrMultipleRows)
				require.False(t, ok)
			}
		})
	}
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
