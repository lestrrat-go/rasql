package query

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type typedTestRow struct{}

func typedTestColumns(t *testing.T) (TypedColumn[typedTestRow, int64], NullableColumn[typedTestRow, *string]) {
	t.Helper()
	table, err := NewTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	return TypedColumnOf[typedTestRow, int64](table.Column("id")), NullableColumnOf[typedTestRow, *string](table.Column("email"))
}

func TestTypedPredicatesNormalizeToExistingAST(t *testing.T) {
	id, email := typedTestColumns(t)
	predicate := AndPredicates(EqualValue(id, int64(7)), IsNotNull(email), InValues(id, int64(7), int64(8)))
	logical, ok := predicate.Expression().(Logical)
	require.True(t, ok)
	require.Len(t, logical.Expressions(), 3)
	require.Equal(t, id.Ref(), id.Ref())
	require.Equal(t, "email", IsNull(email).Expression().(ColumnRef).Name())
	require.Equal(t, "id", AssignValue(id, int64(9)).Column().Name())
}

func TestTypedJoinRequiresSameValueType(t *testing.T) {
	id, _ := typedTestColumns(t)
	other, _ := typedTestColumns(t)
	join := TypedInnerJoin(id.Source(), EqualColumns(id, other))
	require.Equal(t, JoinInner, join.Join().Type())
}
