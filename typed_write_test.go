package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
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
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(int64(42), "ada@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := rasql.Insert(t.Context(), client, users, user{ID: 42, Email: "ada@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestUpdateExecutesTypedRow(t *testing.T) {
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
	mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
		WithArgs("grace@example.com", int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := rasql.Update(t.Context(), client, users, user{ID: 42, Email: "grace@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestUpdateRejectsTableWithoutPrimaryKey(t *testing.T) {
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)

	_, err = rasql.Update(t.Context(), rasql.Client{}, users, user{ID: 42, Email: "grace@example.com"})
	require.ErrorContains(t, err, "has no primary key")
}

func TestUpdateMatchesCompositePrimaryKey(t *testing.T) {
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
		Name: "memberships",
		Columns: []schema.Column{
			{Name: "account_id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "role", Type: schema.TypeText},
		},
		PrimaryKey: []string{"account_id", "user_id"},
	}
	type membership struct {
		AccountID int64  `rasql:"account_id"`
		UserID    int64  `rasql:"user_id"`
		Role      string `rasql:"role"`
	}
	memberships, err := rasql.NewTable[membership](table)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE \"memberships\" SET \"role\" = $1 WHERE ((\"memberships\".\"account_id\" = $2) AND (\"memberships\".\"user_id\" = $3))").
		WithArgs("admin", int64(10), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), client, memberships, membership{AccountID: 10, UserID: 42, Role: "admin"})
	require.NoError(t, err)
}

func TestInsertRejectsMissingTaggedColumn(t *testing.T) {
	type user struct {
		ID int64 `rasql:"id"`
	}
	table := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	users, err := rasql.NewTable[user](table)
	require.NoError(t, err)

	_, err = rasql.Insert(t.Context(), rasql.Client{}, users, user{ID: 42})
	require.ErrorContains(t, err, "no field tagged for column \"email\"")
}
