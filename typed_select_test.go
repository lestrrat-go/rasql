package rasql_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestTypedSelectOneStopsAfterSecondRow(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.NewTable[user](schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	thirdRowError := errors.New("third row was read")
	mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow(int64(1)).
			AddRow(int64(2)).
			AddRow(int64(3)).
			RowError(2, thirdRowError)).
		RowsWillBeClosed()

	_, err = rasql.SelectFrom(client, users).One(t.Context())
	require.EqualError(t, err, "rasql: expected one row, got more than one")
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	require.NotErrorIs(t, err, rasql.ErrNoRows)
	require.NotErrorIs(t, err, sql.ErrNoRows)
	require.NotErrorIs(t, err, thirdRowError)
}

func TestTypedSelectOneNoRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.NewTable[user](schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"})).
		RowsWillBeClosed()

	_, err = rasql.SelectFrom(client, users).One(t.Context())
	require.EqualError(t, err, "rasql: expected one row, got none")
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NotErrorIs(t, err, rasql.ErrMultipleRows)
}

func TestTypedSelectOneQueryFailureIsNotNoRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.NewTable[user](schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	driverError := errors.New("connection reset by peer")
	mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\"").
		WillReturnError(driverError)

	_, err = rasql.SelectFrom(client, users).One(t.Context())
	require.NotErrorIs(t, err, rasql.ErrNoRows)
	require.NotErrorIs(t, err, sql.ErrNoRows)
	require.ErrorIs(t, err, driverError)
}

func TestOneSentinelsAreDistinct(t *testing.T) {
	require.NotErrorIs(t, rasql.ErrMultipleRows, sql.ErrNoRows)
	require.NotErrorIs(t, rasql.ErrMultipleRows, rasql.ErrNoRows)
}
