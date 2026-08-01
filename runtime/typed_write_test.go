package runtime_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/runtime"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestInsertExecutesTypedRow(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := runtime.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	reference, err := query.NewTableRef(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := runtime.NewTable[user](reference)
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(int64(42), "ada@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := runtime.Insert(t.Context(), client, users, user{ID: 42, Email: "ada@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestInsertRejectsMissingTaggedColumn(t *testing.T) {
	type user struct {
		ID int64 `rasql:"id"`
	}
	reference, err := query.NewTableRef(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	users, err := runtime.NewTable[user](reference)
	require.NoError(t, err)

	_, err = runtime.Insert(t.Context(), runtime.Client{}, users, user{ID: 42})
	require.ErrorContains(t, err, "no field tagged for column \"email\"")
}
