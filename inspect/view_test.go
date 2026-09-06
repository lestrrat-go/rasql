package inspect_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteInspectorReadsViewsAsReadOnlyObjects(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER NOT NULL, name TEXT)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE VIEW active_users AS SELECT id, name FROM users")
	require.NoError(t, err)
	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	tableNames, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Len(t, tableNames, 1)
	require.Equal(t, "users", tableNames[0].Name)
	objects, err := inspector.ObjectNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.ObjectName{{Schema: "main", Name: "active_users", Kind: schema.ObjectView}, {Schema: "main", Name: "users", Kind: schema.ObjectTable}}, objects)
	view, err := inspector.Object(t.Context(), "active_users")
	require.NoError(t, err)
	require.Equal(t, schema.ObjectView, view.EffectiveKind())
	require.True(t, view.Supports(schema.OperationRead))
	require.False(t, view.Supports(schema.OperationInsert))
	_, err = inspector.Table(t.Context(), "active_users")
	require.Error(t, err)

	tables, err := catalog.FromQueryer(t.Context(), database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Len(t, tables, 1)
	tables, err = catalog.FromQueryer(t.Context(), database, catalog.Options{Dialect: dialect.SQLite(), IncludeViews: true})
	require.NoError(t, err)
	require.Len(t, tables, 2)
	require.Equal(t, schema.ObjectView, tables[0].EffectiveKind())
}
