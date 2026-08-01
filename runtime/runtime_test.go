package runtime_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/runtime"
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

	client, err := runtime.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	rows, err := client.Query(t.Context(), statement)
	require.NoError(t, err)
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

func TestNewRejectsNilDependencies(t *testing.T) {
	_, err := runtime.New(nil, dialect.PostgreSQL())
	require.Error(t, err)

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	_, err = runtime.New(database, nil)
	require.Error(t, err)
}

func selectStatement(t *testing.T) query.Select {
	t.Helper()
	users, err := query.NewTableRef(schema.Table{
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
