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
	defer db.Close()
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
