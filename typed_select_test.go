package rasql_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
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

func TestTypedSelectCombinesPredicates(t *testing.T) {
	t.Run("WhereEqual then Where combine with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(clientForBuild(t), users).
			WhereEqual(id, 42).
			Where(query.Like(email, query.Bind("%@example.com"))).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" LIKE $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "%@example.com"}, statement.Args())
	})

	t.Run("a lone Or predicate survives unwrapped", func(t *testing.T) {
		users := deleteUsersTable(t)
		email, err := users.Column("email")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(clientForBuild(t), users).
			Where(query.Or(
				query.Equal(email, query.Bind("ada@example.com")),
				query.Equal(email, query.Bind("bob@example.com")),
			)).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."email" = $1) OR ("users"."email" = $2))`,
			statement.SQL())
	})

	t.Run("predicates from a joined table combine", func(t *testing.T) {
		users := deleteUsersTable(t)
		type order struct {
			ID     int64 `rasql:"id"`
			UserID int64 `rasql:"user_id"`
		}
		orders, err := rasql.NewTable[order](schema.Table{
			Name: "orders",
			Columns: []schema.Column{
				{Name: "id", Type: schema.TypeInteger},
				{Name: "user_id", Type: schema.TypeInteger},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		userID, err := users.Column("id")
		require.NoError(t, err)
		ordersUserID, err := orders.Column("user_id")
		require.NoError(t, err)

		// WhereEqual takes a primary-table column, Where takes a column of the
		// joined table; accumulating them must not bypass the "column must be
		// in the statement" validation for either.
		statement, err := rasql.SelectFrom(clientForBuild(t), users).
			Join(rasql.InnerJoin(orders, query.Equal(userID, ordersUserID))).
			WhereEqual(userID, 42).
			Where(query.GreaterThan(ordersUserID, query.Bind(0))).
			Build()
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id") WHERE (("users"."id" = $1) AND ("orders"."user_id" > $2))`,
			statement.SQL())
		require.Equal(t, []any{42, 0}, statement.Args())
	})
}
