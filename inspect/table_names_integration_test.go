//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLInspectorReadsTableNamesAgainstLiveDatabase confirms
// TableNames' pg_catalog query against a real server, not only the sqlmock
// fixture in inspect_test.go: that ordinary tables in current_schema() come
// back sorted with every TableName.Schema empty, and that a view of the same
// name pattern is excluded.
func TestPostgreSQLInspectorReadsTableNamesAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE zebras (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE armadillos (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE VIEW zebra_view AS SELECT id FROM zebras")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	refs, err := inspector.TableNames(ctx)
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Name: "armadillos"}, {Name: "zebras"}}, refs)
}

// TestMySQLInspectorReadsTableNamesAgainstLiveDatabase is the MySQL
// counterpart: information_schema.tables filtered to table_type =
// 'BASE TABLE' must exclude a view scoped to the same DATABASE(), and every
// TableName.Schema stays empty here too.
func TestMySQLInspectorReadsTableNamesAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE zebras (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE armadillos (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE VIEW zebra_view AS SELECT id FROM zebras")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	refs, err := inspector.TableNames(ctx)
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Name: "armadillos"}, {Name: "zebras"}}, refs)
}

func TestPostgreSQLInspectorReadsViewObjectsAgainstLiveDatabase(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	mustExec(t, t.Context(), database, "CREATE TABLE view_source (id integer, name text)")
	mustExec(t, t.Context(), database, "CREATE VIEW view_read AS SELECT id, name FROM view_source")
	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	view, err := inspector.Object(t.Context(), "view_read")
	require.NoError(t, err)
	require.Equal(t, schema.ObjectView, view.EffectiveKind())
	require.Equal(t, schema.OperationRead, view.Operations)
	require.Equal(t, []string{"id", "name"}, []string{view.Columns[0].Name, view.Columns[1].Name})
	require.Equal(t, []schema.ColumnType{schema.IntegerType{}, schema.TextType{}}, []schema.ColumnType{view.Columns[0].Type, view.Columns[1].Type})
	require.True(t, view.Columns[0].Nullable)
	objects, err := inspector.ObjectNames(t.Context())
	require.NoError(t, err)
	require.Contains(t, objects, inspect.ObjectName{Name: "view_read", Kind: schema.ObjectView})
	rows, err := database.QueryContext(t.Context(), "SELECT id, name FROM view_read")
	require.NoError(t, err)
	require.NoError(t, rows.Close())
}

func TestMySQLInspectorReadsViewObjectsAgainstLiveDatabase(t *testing.T) {
	database := dbtest.MySQLDB(t)
	mustExec(t, t.Context(), database, "CREATE TABLE view_source (id integer, name text)")
	mustExec(t, t.Context(), database, "CREATE VIEW view_read AS SELECT id, name FROM view_source")
	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	view, err := inspector.Object(t.Context(), "view_read")
	require.NoError(t, err)
	require.Equal(t, schema.ObjectView, view.EffectiveKind())
	require.Equal(t, schema.OperationRead, view.Operations)
	require.Equal(t, []string{"id", "name"}, []string{view.Columns[0].Name, view.Columns[1].Name})
	require.Equal(t, schema.IntegerType{}, view.Columns[0].Type)
	require.Equal(t, schema.TextType{}, view.Columns[1].Type)
	require.True(t, view.Columns[0].Nullable)
	objects, err := inspector.ObjectNames(t.Context())
	require.NoError(t, err)
	require.Contains(t, objects, inspect.ObjectName{Name: "view_read", Kind: schema.ObjectView})
	rows, err := database.QueryContext(t.Context(), "SELECT id, name FROM view_read")
	require.NoError(t, err)
	require.NoError(t, rows.Close())
}
