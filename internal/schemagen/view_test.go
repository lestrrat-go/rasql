package schemagen_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPackageSourceGeneratesReadOnlyViewSurface(t *testing.T) {
	text := compactRenderedSource(t, schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
	})
	require.Contains(t, text, "rasql.ReadTable[ActiveUsersRow]")
	require.Contains(t, text, "rasql.MustReadTableOf[ActiveUsersRow]")
	require.NotContains(t, text, "rasql.Table[ActiveUsersRow]")
	require.NotContains(t, text, "rasql.MustTableOf[ActiveUsersRow]")
	require.Contains(t, text, "ID rasql.Column[ActiveUsersRow, int64]")
	require.NotContains(t, text, "ActiveUsersCreate")
	require.NotContains(t, text, "ActiveUsersPatch")
}
