package catalogread_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func sqliteProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	return p
}

func TestReadSQLiteScopePoliciesAndVirtualObjects(t *testing.T) {
	db, err := sql.Open("sqlite", "file:catalogread_scope?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(context.Background(), `
		CREATE TABLE ordinary (id INTEGER);
		CREATE TABLE history (id INTEGER);
		CREATE VIEW visible AS SELECT id FROM ordinary;
		CREATE VIRTUAL TABLE search USING fts5(body);
	`)
	require.NoError(t, err)

	result, err := catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{
		IncludeViews: true,
		HistoryTable: schema.ObjectName{Name: "history"},
	})
	require.NoError(t, err)
	got := make(map[string]schema.ObjectKind, len(result.Tables))
	for _, table := range result.Tables {
		got[table.Name] = table.EffectiveKind()
	}
	require.NotContains(t, got, "history")
	require.Equal(t, schema.ObjectView, got["visible"])
	require.Contains(t, got, "search")
	require.Empty(t, result.Unresolved)

	result, err = catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{
		Include: []schema.ObjectName{{Name: "ordinary"}},
		Exclude: []schema.ObjectName{{Name: "history"}},
	})
	require.NoError(t, err)
	require.Len(t, result.Tables, 1)
	require.Equal(t, "ordinary", result.Tables[0].Name)
}

// TestReadSQLiteScopeNamespacesEnumeratesAttachedDatabase pins catalogNames'
// new namespace enumeration against SQLite's ATTACH DATABASE, the same
// technique catalog/namespace_test.go uses to give a single-engine table
// two independent namespaces without a second live server. It cannot stand
// in for the live PostgreSQL/MySQL test: it proves the Go-side selection
// logic picks the right rows out of ObjectNamesIn, not that a real schema or
// database boundary reports what this package assumes.
func TestReadSQLiteScopeNamespacesEnumeratesAttachedDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", "file:catalogread_namespaces?mode=memory&cache=shared")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(context.Background(), "CREATE TABLE events (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "ATTACH DATABASE ':memory:' AS audit")
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "CREATE TABLE audit.events (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	result, err := catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{
		Namespaces: []string{"audit"},
	})
	require.NoError(t, err)
	require.Len(t, result.Tables, 1)
	require.Equal(t, "audit", result.Tables[0].Schema)
	require.Equal(t, "events", result.Tables[0].Name)

	result, err = catalogread.Read(context.Background(), db, sqliteProfile(t), catalogread.Scope{
		Namespaces: []string{"main", "audit"},
	})
	require.NoError(t, err)
	require.Len(t, result.Tables, 2)
	require.Equal(t, "audit", result.Tables[0].Schema)
	require.Equal(t, "main", result.Tables[1].Schema)
}

func TestReadRejectsInvalidScopeAndCustomBeforeBegin(t *testing.T) {
	p := sqliteProfile(t)
	_, err := catalogread.Read(context.Background(), nil, p, catalogread.Scope{
		Include: []schema.ObjectName{{Name: "same"}},
		Exclude: []schema.ObjectName{{Name: "same"}},
	})
	require.Error(t, err)

	custom, err := engineprofile.New("custom:test", engineprofile.Custom, "test", engineprofile.Version{}, engineprofile.Capabilities{}, engineprofile.Limits{MaxBindParameters: 1})
	require.NoError(t, err)
	_, err = catalogread.Read(context.Background(), nil, custom, catalogread.Scope{})
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
}
