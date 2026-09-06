package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestExecAcceptsPointerWriteStatements(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, rasql.DB, *sql.DB, query.TableRef)
	}{
		{name: "insert", run: testPointerInsert},
		{name: "update", run: testPointerUpdate},
		{name: "delete", run: testPointerDelete},
		{name: "upsert", run: testPointerUpsert},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`)
			require.NoError(t, err)
			db, err := rasql.New(database, dialect.SQLite())
			require.NoError(t, err)
			testCase.run(t, db, database, pointerWriteTable(t))
		})
	}
}

func testPointerInsert(t *testing.T, db rasql.DB, database *sql.DB, users query.TableRef) {
	id, email := users.Column("id"), users.Column("email")
	statement, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, &statement)
	require.NoError(t, err)
	var stored string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT email FROM users WHERE id = 1`).Scan(&stored))
	require.Equal(t, "ada@example.com", stored)
}

func testPointerUpdate(t *testing.T, db rasql.DB, database *sql.DB, users query.TableRef) {
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (2, 'before@example.com')`)
	require.NoError(t, err)
	id, email := users.Column("id"), users.Column("email")
	statement, err := query.NewUpdate(users, query.Set(email, query.Bind("after@example.com")))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(2)))
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, &statement)
	require.NoError(t, err)
	var stored string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT email FROM users WHERE id = 2`).Scan(&stored))
	require.Equal(t, "after@example.com", stored)
}

func testPointerDelete(t *testing.T, db rasql.DB, database *sql.DB, users query.TableRef) {
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (3, 'delete@example.com')`)
	require.NoError(t, err)
	id := users.Column("id")
	statement, err := query.NewDelete(users)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(3)))
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, &statement)
	require.NoError(t, err)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM users WHERE id = 3`).Scan(&count))
	require.Zero(t, count)
}

func testPointerUpsert(t *testing.T, db rasql.DB, database *sql.DB, users query.TableRef) {
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (4, 'before@example.com')`)
	require.NoError(t, err)
	id, email := users.Column("id"), users.Column("email")
	insert, err := query.NewInsert(users, query.Set(id, 4), query.Set(email, "after@example.com"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, &statement)
	require.NoError(t, err)
	var stored string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT email FROM users WHERE id = 4`).Scan(&stored))
	require.Equal(t, "after@example.com", stored)
}

func TestExecRejectsNilWriteStatements(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)

	tests := []query.WriteStatement{
		(*query.Insert)(nil),
		(*query.Update)(nil),
		(*query.Delete)(nil),
		(*query.Upsert)(nil),
		nil,
	}
	for _, statement := range tests {
		_, err = rasql.Exec(t.Context(), db, statement)
		require.EqualError(t, err, "rasql: render write statement: render: write statement must not be nil")
	}

	var returning *query.Insert
	_, err = rasql.QueryWriteOne[struct{}](t.Context(), db, returning)
	require.EqualError(t, err, "rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
}

func pointerWriteTable(t *testing.T) query.TableRef {
	t.Helper()
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}
