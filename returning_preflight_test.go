package rasql_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type returningPreflightUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

type returningPreflightPartial struct {
	ID int64 `rasql:"id"`
}

type rejectingReturningScanner struct{}

func (*rejectingReturningScanner) ScanDestinations([]string) ([]any, error) {
	return nil, errors.New("unsupported returning column")
}

type deferredReturningScanner struct {
	value any
}

func (s *deferredReturningScanner) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	for index := range destinations {
		destinations[index] = &s.value
	}
	return destinations, nil
}

func returningPreflightDB(t *testing.T) (rasql.DB, *sql.DB, query.TableRef) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`)
	require.NoError(t, err)
	return db, database, table
}

func TestReturningPreflightRejectsInvalidReflectiveResultsBeforeWrites(t *testing.T) {
	db, database, users := returningPreflightDB(t)
	id := users.Column("id")
	email := users.Column("email")
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (1, 'ada@example.com')`)
	require.NoError(t, err)

	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, int64(1)))
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithReturning(id)
	require.NoError(t, err)
	for _, run := range []func() error{
		func() error {
			_, err := rasql.QueryWriteAll[returningPreflightUser](t.Context(), db, deleteStatement)
			return err
		},
		func() error {
			_, err := rasql.QueryWriteOne[returningPreflightUser](t.Context(), db, deleteStatement)
			return err
		},
	} {
		require.ErrorContains(t, run(), `row: column "email" is not present`)
	}
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM users`).Scan(&count))
	require.Equal(t, 1, count)

	insertStatement, err := query.NewInsert(users, query.Set(email, "grace@example.com"))
	require.NoError(t, err)
	insertStatement, err = insertStatement.WithReturning(id)
	require.NoError(t, err)
	for _, run := range []func() error{
		func() error { _, err := rasql.QueryWriteAll[int](t.Context(), db, insertStatement); return err },
		func() error { _, err := rasql.QueryWriteOne[int](t.Context(), db, insertStatement); return err },
	} {
		require.EqualError(t, run(), "row: decode destination int must be a struct")
	}
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM users`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestReturningPreflightAcceptsPartialAndCustomResults(t *testing.T) {
	db, database, users := returningPreflightDB(t)
	id := users.Column("id")
	email := users.Column("email")
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (1, 'ada@example.com')`)
	require.NoError(t, err)

	insertStatement, err := query.NewInsert(users, query.Set(email, "grace@example.com"))
	require.NoError(t, err)
	insertStatement, err = insertStatement.WithReturning(id)
	require.NoError(t, err)
	result, err := rasql.QueryWriteOne[returningPreflightPartial](t.Context(), db, insertStatement)
	require.NoError(t, err)
	require.NotZero(t, result.ID)

	rejecting, err := query.NewDelete(users)
	require.NoError(t, err)
	rejecting, err = rejecting.WithReturning(id)
	require.NoError(t, err)
	_, err = rasql.QueryWriteOne[rejectingReturningScanner](t.Context(), db, rejecting)
	require.ErrorContains(t, err, "rasql: configure result scan: unsupported returning column")
}

func TestReturningPreflightDefersUnknownExpressionNames(t *testing.T) {
	db, database, users := returningPreflightDB(t)
	id := users.Column("id")
	email := users.Column("email")
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (1, 'ada@example.com')`)
	require.NoError(t, err)

	statement, err := query.NewDelete(users)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, int64(1)))
	require.NoError(t, err)
	statement, err = statement.WithReturning(query.Lower(email))
	require.NoError(t, err)
	result, err := rasql.QueryWriteOne[deferredReturningScanner](t.Context(), db, statement)
	require.NoError(t, err)
	require.Equal(t, "ada@example.com", result.value)
}
