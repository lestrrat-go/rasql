//go:build unix

package schemasource_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/stretchr/testify/require"
)

// TestReadClearsThePostgreSQLConnectedSchemaAndKeepsAnother reads two tables from a live
// PostgreSQL server, one in the schema the connection is already in and one in a schema it is not,
// and pins the namespace each descriptor carries.
//
// Scoping a read to "public" is the case the rule changes: inspect's default enumeration already
// leaves Schema empty, while a read that names the schema records it, so the same table used to
// generate as "public"."probe_default" or as "probe_default" depending only on how the read was
// scoped. Both now generate as "probe_default". The second schema is named through
// dbtest.UniqueName and sits inside the per-run database dbtest drops afterward.
func TestReadClearsThePostgreSQLConnectedSchemaAndKeepsAnother(t *testing.T) {
	cfg := dbtest.PostgreSQLConfig(t)
	setup := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { require.NoError(t, setup.Close()) })

	other := dbtest.UniqueName(t, "rasql_ns")
	_, err := setup.ExecContext(t.Context(), `CREATE TABLE probe_default (id bigint PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = setup.ExecContext(t.Context(), fmt.Sprintf(`CREATE SCHEMA %q`, other))
	require.NoError(t, err)
	_, err = setup.ExecContext(t.Context(), fmt.Sprintf(
		`CREATE TABLE %q.probe_other (id bigint PRIMARY KEY, owner_id bigint NOT NULL REFERENCES probe_default (id))`, other))
	require.NoError(t, err)

	openPostgreSQL := func() *sql.DB { return stdlib.OpenDB(*cfg) }
	require.Equal(t, "", objectOf(t, "postgresql", openPostgreSQL(), "public", "probe_default").Schema,
		"a table in the schema the connection is already in generates unqualified")

	qualified := objectOf(t, "postgresql", openPostgreSQL(), other, "probe_other")
	require.Equal(t, other, qualified.Schema,
		"a table in another schema keeps the name a statement needs to reach it")
	require.Equal(t, "", foreignReferenceOf(t, qualified).Schema,
		"a foreign key pointing back into the connected schema names no schema either")
}

// TestReadClearsTheMySQLConnectedDatabaseAndKeepsAnother is the MySQL half. A MySQL namespace is a
// database, so the namespace the connection is using is whatever DATABASE() answers. The first
// read runs on a connection inside the per-run database, and the second reads the same table from
// a connection that selected no database at all, where DATABASE() answers NULL and the database
// name stays in the descriptor.
func TestReadClearsTheMySQLConnectedDatabaseAndKeepsAnother(t *testing.T) {
	cfg := dbtest.MySQLConfig(t)
	setup := openMySQL(t, cfg)
	t.Cleanup(func() { require.NoError(t, setup.Close()) })

	_, err := setup.ExecContext(t.Context(), "CREATE TABLE probe_default (id BIGINT NOT NULL PRIMARY KEY)")
	require.NoError(t, err)

	unselected := cfg.Clone()
	unselected.DBName = ""
	require.Equal(t, "", objectOf(t, "mysql", openMySQL(t, cfg), cfg.DBName, "probe_default").Schema,
		"a table in the database the connection selected generates unqualified")
	require.Equal(t, cfg.DBName, objectOf(t, "mysql", openMySQL(t, unselected), cfg.DBName, "probe_default").Schema,
		"the same table read from a connection that selected no database keeps its database name")
}

// objectOf reads one namespace through the whole generate path and returns the descriptor it
// produced for table. Only the opener is replaced, with one that hands back db: dbtest returns an
// already-parsed connection configuration and its package doc forbids rebuilding a DSN string out
// of one, and schemasource.Read closes whatever the opener gave it, so each call takes a
// connection of its own. Everything else -- profile discovery, the catalog read, and the query
// that asks the server which namespace it is connected to -- runs against the live server.
func objectOf(t *testing.T, dialect string, db *sql.DB, namespace, table string) compilerir.PhysicalObject {
	t.Helper()
	deps := schemasource.DefaultDependencies()
	deps.Opener = fakeDBOpener{db: db}
	req := schemasource.ReadRequest{
		ModuleRoot: t.TempDir(),
		Dialect:    dialect,
		DSN:        "supplied-by-the-opener",
		Scope:      catalogread.Scope{Namespaces: []string{namespace}},
	}
	result, err := schemasource.Read(t.Context(), req, deps)
	require.NoError(t, err)
	for _, object := range result.Catalog.Objects {
		if object.Name == table {
			return object
		}
	}
	t.Fatalf("table %q was not read from namespace %q", table, namespace)
	return compilerir.PhysicalObject{}
}

// foreignReferenceOf returns the one foreign key object carries, and fails the test when it
// carries none or more than one.
func foreignReferenceOf(t *testing.T, object compilerir.PhysicalObject) compilerir.ForeignReference {
	t.Helper()
	var found []compilerir.ForeignReference
	for _, constraint := range object.Constraints {
		if constraint.Kind == "foreign_key" && constraint.Reference != nil {
			found = append(found, *constraint.Reference)
		}
	}
	require.Len(t, found, 1, "table %q", object.Name)
	return found[0]
}

func openMySQL(t *testing.T, cfg *mysql.Config) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	return db
}
