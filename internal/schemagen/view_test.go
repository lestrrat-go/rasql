package schemagen_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPackageSourceGeneratesReadOnlyViewSurface(t *testing.T) {
	source, err := schemagen.PackageSource("generated", schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
	})
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "rasql.ReadTable[ActiveUsersRow]")
	require.Contains(t, text, "rasql.ReadTableFrom[ActiveUsersRow]")
	require.NotContains(t, text, "rasql.Table[ActiveUsersRow]")
	require.NotContains(t, text, "rasql.As(t.Table")
	require.True(t, strings.Contains(text, "func (t ActiveUsersTable) ID() query.TypedColumn[ActiveUsersRow, int64]"))
	require.True(t, strings.Contains(text, "func (t ActiveUsersTable) IDRef() rasql.ColumnRef"))
	require.NotContains(t, text, "ActiveUsersCreate")
	require.NotContains(t, text, "ActiveUsersPatch")
}
