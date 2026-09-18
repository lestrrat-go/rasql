package namespace_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/namespace"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEngineID(t *testing.T) {
	testCases := []struct {
		dialect string
		want    engineprofile.EngineID
	}{
		{dialect: "postgresql", want: engineprofile.PostgreSQL},
		{dialect: "postgres", want: engineprofile.PostgreSQL},
		{dialect: "PostgreSQL", want: engineprofile.PostgreSQL},
		{dialect: "mysql", want: engineprofile.MySQL},
		{dialect: "MySQL", want: engineprofile.MySQL},
		{dialect: "sqlite", want: engineprofile.SQLite},
		{dialect: "sqlite3", want: engineprofile.SQLite},
		{dialect: "unknown", want: engineprofile.SQLite},
	}
	for _, testCase := range testCases {
		t.Run(testCase.dialect, func(t *testing.T) {
			require.Equal(t, testCase.want, namespace.EngineID(testCase.dialect))
		})
	}
}

func TestDefaultQuery(t *testing.T) {
	query, ok := namespace.DefaultQuery(engineprofile.PostgreSQL)
	require.True(t, ok)
	require.Equal(t, "SELECT current_schema()", query)

	query, ok = namespace.DefaultQuery(engineprofile.MySQL)
	require.True(t, ok)
	require.Equal(t, "SELECT DATABASE()", query)

	query, ok = namespace.DefaultQuery(engineprofile.SQLite)
	require.True(t, ok)
	require.Equal(t, "SELECT name FROM pragma_database_list WHERE seq = 0", query)

	_, ok = namespace.DefaultQuery(engineprofile.Custom)
	require.False(t, ok, "an engine with no statement of its own reports false")
}

// TestDefaultReadsThroughDBAndThroughTx pins that Default accepts either connection shape
// internal/schemasource and cli/rasqlmigrate's dump command hold: Default takes
// engineprofile.Queryer, which both *sql.DB and *sql.Tx satisfy.
func TestDefaultReadsThroughDBAndThroughTx(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectQuery("SELECT current_schema\\(\\)").WillReturnRows(sqlmock.NewRows([]string{"current_schema"}).AddRow("public"))
	connected, err := namespace.Default(t.Context(), db, engineprofile.PostgreSQL)
	require.NoError(t, err)
	require.Equal(t, "public", connected)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT DATABASE\\(\\)").WillReturnRows(sqlmock.NewRows([]string{"database"}).AddRow("app"))
	mock.ExpectCommit()
	tx, err := db.Begin()
	require.NoError(t, err)
	connected, err = namespace.Default(t.Context(), tx, engineprofile.MySQL)
	require.NoError(t, err)
	require.Equal(t, "app", connected)
	require.NoError(t, tx.Commit())

	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDefaultReturnsEmptyForANullAnswer covers the answer neither PostgreSQL nor MySQL gives on an
// ordinary connection: current_schema() or DATABASE() can themselves answer NULL, and Default
// reports that as the empty string rather than an error.
func TestDefaultReturnsEmptyForANullAnswer(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectQuery("SELECT DATABASE\\(\\)").WillReturnRows(sqlmock.NewRows([]string{"database"}).AddRow(nil))
	connected, err := namespace.Default(t.Context(), db, engineprofile.MySQL)
	require.NoError(t, err)
	require.Equal(t, "", connected)

	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDefaultRunsNoQueryForAnUnknownEngine covers engineprofile.Custom, which DefaultQuery has no
// statement for: Default must return the empty namespace and no error without issuing any query at
// all.
func TestDefaultRunsNoQueryForAnUnknownEngine(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	connected, err := namespace.Default(t.Context(), db, engineprofile.Custom)
	require.NoError(t, err)
	require.Equal(t, "", connected)

	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUnqualifyClearsSchemaAndReferencedSchema(t *testing.T) {
	tables := []schema.TableDef{
		{Schema: "public", Name: "teams"},
		{
			Schema: "public",
			Name:   "members",
			ForeignKeys: []schema.ForeignKeyDef{
				{ReferencedSchema: "public", ReferencedTable: "teams"},
				{ReferencedSchema: "audit", ReferencedTable: "events"},
			},
		},
		{Schema: "audit", Name: "events"},
	}
	cleared := namespace.Unqualify(tables, "public")
	require.Equal(t, "", cleared[0].Schema)
	require.Equal(t, "", cleared[1].Schema)
	require.Equal(t, "", cleared[1].ForeignKeys[0].ReferencedSchema, "a foreign key naming the cleared namespace is cleared too")
	require.Equal(t, "audit", cleared[1].ForeignKeys[1].ReferencedSchema, "a foreign key naming another namespace survives")
	require.Equal(t, "audit", cleared[2].Schema, "a table in another namespace survives")
}

// TestUnqualifyClearsNothingForAnEmptyNamespace pins the case an unknown engine, or a server that
// answered NULL, leaves Default's caller holding: an empty namespace clears nothing.
func TestUnqualifyClearsNothingForAnEmptyNamespace(t *testing.T) {
	tables := []schema.TableDef{{Schema: "public", Name: "teams"}}
	cleared := namespace.Unqualify(tables, "")
	require.Equal(t, "public", cleared[0].Schema)
}

// TestDefaultAndUnqualifyAgainstARealSQLiteAttachedDatabase reads a real SQLite connection that has
// a second database attached under the name "audit", and pins both exported functions working
// together: Default reports "main", the database an unqualified CREATE TABLE writes into even with
// another database attached, and Unqualify then clears only the descriptor naming it, leaving the
// one in "audit" alone. ATTACH binds to one connection, which is why the pool is capped at one.
func TestDefaultAndUnqualifyAgainstARealSQLiteAttachedDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rwc", filepath.Join(dir, "main.db")))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), fmt.Sprintf("ATTACH DATABASE 'file:%s?mode=rwc' AS audit", filepath.Join(dir, "audit.db")))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "CREATE TABLE audit.events (id INTEGER NOT NULL PRIMARY KEY, owner_id INTEGER NOT NULL)")
	require.NoError(t, err)

	connected, err := namespace.Default(t.Context(), db, namespace.EngineID("sqlite"))
	require.NoError(t, err)
	require.Equal(t, "main", connected)

	tables := []schema.TableDef{
		{Schema: "main", Name: "users"},
		{
			Schema: "audit",
			Name:   "events",
			ForeignKeys: []schema.ForeignKeyDef{
				{ReferencedSchema: "main", ReferencedTable: "users"},
			},
		},
	}
	cleared := namespace.Unqualify(tables, connected)
	require.Equal(t, "", cleared[0].Schema, "the table in \"main\" comes back unqualified")
	require.Equal(t, "audit", cleared[1].Schema, "the table in \"audit\" keeps its namespace")
	require.Equal(t, "", cleared[1].ForeignKeys[0].ReferencedSchema, "a foreign key pointing back into \"main\" names no schema either")
}
