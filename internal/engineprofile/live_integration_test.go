//go:build unix

package engineprofile_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/ddlcompile"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestDiscoverAndReadPostgreSQL(t *testing.T) {
	testLiveEngine(t, dbtest.PostgreSQLDB, engineprofile.PostgreSQL, "postgresql-17")
}
func TestDiscoverAndReadMySQL(t *testing.T) {
	testLiveEngine(t, dbtest.MySQLDB, engineprofile.MySQL, "mysql-8.4")
}

func testLiveEngine(t *testing.T, open func(*testing.T) *sql.DB, engine engineprofile.EngineID, id string) {
	db := open(t)
	p, err := engineprofile.Discover(t.Context(), db, engine, id)
	require.NoError(t, err)
	require.Equal(t, engine, p.Engine)
	require.Equal(t, id, p.ID)

	table, err := schema.NewTableDef("d1_engine_fixture", schema.Integer("id"), schema.Text("value"), schema.PrimaryKey("id"))
	require.NoError(t, err)
	ddl, err := ddlcompile.New(p)
	require.NoError(t, err)
	create, err := ddl.CreateTable(table)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), create[0].SQL(), create[0].Args()...)
	require.NoError(t, err)
	result, err := catalogread.Read(t.Context(), db, p, catalogread.Scope{})
	require.NoError(t, err)
	require.Len(t, result.Tables, 1)
	require.Equal(t, "d1_engine_fixture", result.Tables[0].Name)
	require.Equal(t, p, result.Observed)
}

func TestDiscoverAndReadSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	p, err := engineprofile.Discover(t.Context(), db, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	require.Equal(t, uint16(3), p.Version.Major)
	require.NoError(t, func() error {
		_, e := db.ExecContext(t.Context(), "CREATE TABLE d1_engine_fixture (id INTEGER PRIMARY KEY, value TEXT)")
		return e
	}())
	result, err := catalogread.Read(t.Context(), db, p, catalogread.Scope{})
	require.NoError(t, err)
	require.Len(t, result.Tables, 1)
}
