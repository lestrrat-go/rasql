package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestClientQueryExecutesParameterizedSelect(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	sequence, err := client.Query(t.Context(), statement)
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)

	id, err := row.Int64("id")
	require.NoError(t, err)
	email, err := row.String("email")
	require.NoError(t, err)
	gotID, err := id.Get(rows[0])
	require.NoError(t, err)
	gotEmail, err := email.Get(rows[0])
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	require.Equal(t, "ada@example.com", gotEmail)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClientSelectFromBuildsAndExecutesQuery(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	sequence, err := client.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(t.Context())
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTypedSelectFromDecodesGeneratedRowType(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	rows, err := rasql.SelectFrom(client, users).WhereEqual(id, 42).Query(t.Context())
	require.NoError(t, err)
	decoded := make([]user, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []user{{ID: 42, Email: "ada@example.com"}}, decoded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDecodeQueryFromDecodesProjectedRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	mock.ExpectQuery("SELECT \"users\".\"id\" AS \"user_id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email"}).AddRow(int64(42), "ada@example.com"))

	type summary struct {
		UserID int64
		Email  string
	}
	rows, err := rasql.DecodeQueryFrom[summary](client, users).
		Project(query.Project(id).As("user_id"), query.Project(email)).
		Where(query.Equal(id, query.Bind(42))).
		Query(t.Context())
	require.NoError(t, err)
	decoded := make([]summary, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []summary{{UserID: 42, Email: "ada@example.com"}}, decoded)
}

func TestClientQueryAllowsDebugQueryer(t *testing.T) {
	queryer := &debugQueryer{}
	client, err := rasql.New(queryer, dialect.PostgreSQL())
	require.NoError(t, err)

	sequence, err := client.Query(t.Context(), selectStatement(t))
	rows := collectRows(t, sequence, err)
	require.Empty(t, rows)
	require.Equal(t, "SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)", queryer.query)
	require.Equal(t, []any{42}, queryer.arguments)
}

func TestNewRejectsNilDependencies(t *testing.T) {
	_, err := rasql.New(nil, dialect.PostgreSQL())
	require.Error(t, err)

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	_, err = rasql.New(database, nil)
	require.Error(t, err)
}

func TestClientExecExecutesParameterizedInsert(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	statement, err := query.NewInsert(users, []query.Column{id, email}, []query.Expression{query.Bind(42), query.Bind("ada@example.com")})
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(42, "ada@example.com").
		WillReturnResult(sqlmock.NewResult(42, 1))

	result, err := client.Exec(t.Context(), statement)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClientQueryRenderedExecutesStaticStatement(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement, err := render.Precompiled("SELECT id FROM users WHERE id = $1", 42)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users WHERE id = $1").WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))

	sequence, err := client.QueryRendered(t.Context(), statement)
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClientQueryClosesRowsWhenIterationStops(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(42), "ada@example.com").
			AddRow(int64(43), "bob@example.com")).
		RowsWillBeClosed()

	rows, err := client.Query(t.Context(), statement)
	require.NoError(t, err)
	for result, err := range rows {
		require.NoError(t, err)
		id, err := row.Get[int64](result, "id")
		require.NoError(t, err)
		require.Equal(t, int64(42), id)
		break
	}
}

func TestClientQueryYieldsExecutionError(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	expected := errors.New("database unavailable")
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnError(expected)

	rows, err := client.Query(t.Context(), statement)
	require.NoError(t, err)
	count := 0
	for _, err := range rows {
		require.ErrorIs(t, err, expected)
		count++
	}
	require.Equal(t, 1, count)
}

func TestCreateExecutesTableAndIndexes(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.Index{{
			Name:    "users_email_idx",
			Columns: []string{"email"},
		}},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)
	mock.ExpectExec("CREATE TABLE \"users\" (\"id\" BIGINT NOT NULL, \"email\" TEXT NOT NULL, PRIMARY KEY (\"id\"))").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE INDEX \"users_email_idx\" ON \"users\" (\"email\")").
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, rasql.Create(t.Context(), client, users))
	require.NoError(t, mock.ExpectationsWereMet())
}

func selectStatement(t *testing.T) query.Select {
	t.Helper()
	users, err := query.NewTable(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	return statement
}

type debugQueryer struct {
	query     string
	arguments []any
}

func (q *debugQueryer) QueryContext(_ context.Context, query string, arguments ...any) (*sql.Rows, error) {
	q.query = query
	q.arguments = append([]any(nil), arguments...)
	return nil, nil
}

func collectRows(t *testing.T, sequence iter.Seq2[row.Row, error], queryError error) []row.Row {
	t.Helper()
	require.NoError(t, queryError)
	result := make([]row.Row, 0)
	for value, err := range sequence {
		require.NoError(t, err)
		result = append(result, value)
	}
	return result
}
