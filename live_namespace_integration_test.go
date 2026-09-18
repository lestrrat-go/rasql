//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestMovedTableReachesASecondNamespaceAgainstLiveDatabases proves against
// real PostgreSQL and MySQL servers what InSchema exists for: a table moved at
// run time is written to and read from the namespace it was moved into, not
// the one the connection is sitting in.
//
// A fixture test cannot prove that. It asserts rasql's rendered text back to
// itself and never asks a server which table the text reached, and reaching
// the wrong table is exactly the failure this API is here to prevent. So each
// case creates the same table name twice -- once where the connection already
// is, once in a namespace it creates -- and then requires that a moved
// INSERT lands in the second one while the first stays empty. The two tables
// are indistinguishable by name, so the only thing that can have routed the
// statement is the namespace rendered in front of it.
//
// PostgreSQL's namespace is a schema and MySQL's is a database, which is the
// MySQL tenancy case the whole design is for: one schema copied into
// tenant_0001 and tenant_0002, reached from one generated store.
func TestMovedTableReachesASecondNamespaceAgainstLiveDatabases(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)

		// The schema is created inside the throwaway database dbtest made for
		// this run and drops with it, and it is named through UniqueName so
		// this test can only ever touch a namespace it created itself.
		namespace := dbtest.UniqueName(t, "rasql_ns")
		_, err := database.ExecContext(t.Context(), `CREATE SCHEMA "`+namespace+`"`)
		require.NoError(t, err, "create the second schema this test writes into")

		requireMovedTableReachesNamespace(t, database, dialect.PostgreSQL(), namespace)
	})

	t.Run("mysql", func(t *testing.T) {
		database := dbtest.MySQLDB(t)

		// MySQL has no schema inside a database, so the second namespace is a
		// second database. It sits outside the one dbtest drops for this run,
		// so this test drops it itself; UniqueName is what keeps that drop
		// confined to the database this test created.
		namespace := dbtest.UniqueName(t, "rasql_ns")
		_, err := database.ExecContext(t.Context(), "CREATE DATABASE `"+namespace+"`")
		require.NoError(t, err, "create the second database this test writes into")
		t.Cleanup(func() {
			// t.Context is already cancelled by the time cleanup runs.
			_, err := database.ExecContext(context.Background(), "DROP DATABASE `"+namespace+"`")
			require.NoError(t, err, "drop the second database this test created")
		})

		requireMovedTableReachesNamespace(t, database, dialect.MySQL(), namespace)
	})
}

// requireMovedTableReachesNamespace runs one engine's half of
// TestMovedTableReachesASecondNamespaceAgainstLiveDatabases. namespace must
// already exist and must be one the calling subtest created.
func requireMovedTableReachesNamespace(t *testing.T, database *sql.DB, d dialect.Dialect, namespace string) {
	t.Helper()

	type tenantRow struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}

	ctx := t.Context()
	db, err := rasql.Open(ctx, database, d)
	require.NoError(t, err, "open an executor over the live database")

	tableName := dbtest.UniqueName(t, "rasql_moved_users")
	home, err := rasql.TableOf[tenantRow](schema.TableDef{
		Name: tableName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err, "build the typed table the store would have generated")

	moved, err := home.InSchema(namespace)
	require.NoError(t, err, "move the table into the second namespace")

	// Both tables are created through rasql's own DDL, so the qualified
	// CREATE TABLE is exercised by the server too rather than assumed.
	require.NoError(t, rasql.CreateTable(ctx, db, home), "create the table where the connection already is")
	require.NoError(t, rasql.CreateTable(ctx, db, moved), "create the same table in the second namespace")

	id := query.TypedColumnOf[tenantRow, int64](moved.Ref().Column("id"))
	email := query.TypedColumnOf[tenantRow, string](moved.Ref().Column("email"))
	plan, err := rasql.NewCreatePlan(moved, rasql.SetField(id, int64(1)), rasql.SetField(email, "ada@example.com"))
	require.NoError(t, err, "build a create plan over the moved table")
	_, err = rasql.ExecMutation(ctx, db, plan)
	require.NoError(t, err, "the server must accept an INSERT qualified with a second namespace")

	statement, err := render.SelectFrom(d, moved.Ref()).Select("id", "email").Build()
	require.NoError(t, err)
	rows, err := database.QueryContext(ctx, statement.SQL(), statement.Args()...)
	require.NoError(t, err, "the server must accept a SELECT qualified with a second namespace")
	defer func() { _ = rows.Close() }()

	read := []tenantRow{}
	for rows.Next() {
		var row tenantRow
		require.NoError(t, rows.Scan(&row.ID, &row.Email))
		read = append(read, row)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []tenantRow{{ID: 1, Email: "ada@example.com"}}, read, "the moved table must hold the row the moved INSERT wrote")

	// The point of the whole test: the identically named table where the
	// connection is sitting never saw the write.
	counted, err := render.SelectFrom(d, home.Ref()).Project(query.CountAll().As("count")).Build()
	require.NoError(t, err)
	var homeRows int64
	require.NoError(t, database.QueryRowContext(ctx, counted.SQL(), counted.Args()...).Scan(&homeRows))
	require.Zero(t, homeRows, "the table in the connection's own namespace must be untouched")
}
