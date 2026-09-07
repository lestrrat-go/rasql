package catalog_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestFromQueryerReadsSameNamedAttachedSQLiteTablesByIdentity(t *testing.T) {
	database := mustCreateSQLiteDB(t, "CREATE TABLE events (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
	database.SetMaxOpenConns(1)
	conn, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS audit")
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "CREATE TABLE audit.events (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
	require.NoError(t, err)

	tables, err := catalog.FromQueryer(t.Context(), conn, catalog.Options{
		Dialect:    dialect.SQLite(),
		Namespaces: []string{"main", "audit"},
	})
	require.NoError(t, err)
	require.Len(t, tables, 2)
	require.Equal(t, "audit", tables[0].Schema)
	require.Equal(t, "main", tables[1].Schema)
	require.Equal(t, "events", tables[0].Name)
	require.Equal(t, "events", tables[1].Name)
	require.NotEmpty(t, tables[0].Columns)
	require.NotEmpty(t, tables[1].Columns)

	tables, err = catalog.FromQueryer(t.Context(), conn, catalog.Options{
		Dialect:        dialect.SQLite(),
		Namespaces:     []string{"main", "audit"},
		IncludeObjects: []schema.ObjectName{{Schema: "audit", Name: "events"}},
	})
	require.NoError(t, err)
	require.Len(t, tables, 1)
	require.Equal(t, "audit", tables[0].Schema)

	tables, err = catalog.FromQueryer(t.Context(), conn, catalog.Options{
		Dialect:        dialect.SQLite(),
		Namespaces:     []string{"main", "audit"},
		ExcludeObjects: []schema.ObjectName{{Schema: "audit", Name: "events"}},
	})
	require.NoError(t, err)
	require.Len(t, tables, 1)
	require.Equal(t, "main", tables[0].Schema)
}

func TestFromQueryerRejectsAmbiguousLegacyIncludeAcrossNamespaces(t *testing.T) {
	database := mustCreateSQLiteDB(t, "CREATE TABLE events (id INTEGER PRIMARY KEY)")
	database.SetMaxOpenConns(1)
	conn, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS audit")
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "CREATE TABLE audit.events (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	_, err = catalog.FromQueryer(t.Context(), conn, catalog.Options{
		Dialect:    dialect.SQLite(),
		Namespaces: []string{"main", "audit"},
		Include:    []string{"events"},
	})
	require.ErrorContains(t, err, "is ambiguous")
	_, err = catalog.FromQueryer(t.Context(), conn, catalog.Options{
		Dialect:    dialect.SQLite(),
		Namespaces: []string{"main", "audit"},
		Exclude:    []string{"events"},
	})
	require.ErrorContains(t, err, "excluded table \"events\" is ambiguous")
}

func TestFromQueryerDefaultHistoryOnlySkipsDefaultSQLiteIdentity(t *testing.T) {
	database := mustCreateSQLiteDB(t, "CREATE TABLE rasql_schema_migrations (id INTEGER PRIMARY KEY)")
	database.SetMaxOpenConns(1)
	conn, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS audit")
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "CREATE TABLE audit.rasql_schema_migrations (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	tables, err := catalog.FromQueryer(t.Context(), conn, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Len(t, tables, 1)
	require.Equal(t, "audit", tables[0].Schema)

	_, err = catalog.FromQueryer(t.Context(), conn, catalog.Options{
		Dialect:        dialect.SQLite(),
		ExcludeObjects: []schema.ObjectName{{Schema: "audit", Name: "rasql_schema_migrations"}},
	})
	require.ErrorIs(t, err, catalog.ErrNoTables)
}
