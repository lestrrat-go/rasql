package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type deleteUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

func deleteUsersTable(t *testing.T) rasql.Table[deleteUser] {
	t.Helper()

	users, err := rasql.NewTable[deleteUser](schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}

func TestDeleteFrom(t *testing.T) {
	t.Run("WhereEqual deletes matching rows", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		mock.ExpectExec("DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1)").
			WithArgs(42).
			WillReturnResult(sqlmock.NewResult(0, 1))

		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		result, err := rasql.DeleteFrom(client, users).WhereEqual(id, 42).Exec(t.Context())
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
	})

	t.Run("Where takes a query expression", func(t *testing.T) {
		users := deleteUsersTable(t)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := rasql.DeleteFrom(clientForBuild(t), users).
			Where(query.Equal(email, query.Bind("ada@example.com"))).
			Build()
		require.NoError(t, err)
		require.Equal(t, "DELETE FROM \"users\" WHERE (\"users\".\"email\" = $1)", statement.SQL())
		require.Equal(t, []any{"ada@example.com"}, statement.Args())
	})

	t.Run("no predicate deletes every row", func(t *testing.T) {
		statement, err := rasql.DeleteFrom(clientForBuild(t), deleteUsersTable(t)).Build()
		require.NoError(t, err)
		require.Equal(t, "DELETE FROM \"users\"", statement.SQL())
	})

	t.Run("column from another table reports an error", func(t *testing.T) {
		other, err := rasql.NewTable[deleteUser](schema.Table{
			Name:       "archived_users",
			Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		archivedID, err := other.Column("id")
		require.NoError(t, err)

		_, err = rasql.DeleteFrom(clientForBuild(t), deleteUsersTable(t)).WhereEqual(archivedID, 1).Build()
		require.ErrorContains(t, err, "archived_users")
	})

	t.Run("nil expression reports an error", func(t *testing.T) {
		_, err := rasql.DeleteFrom(clientForBuild(t), deleteUsersTable(t)).Where(nil).Build()
		require.ErrorContains(t, err, "must not be nil")
	})

	t.Run("Client.DeleteFrom builds the same statement", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		client := clientForBuild(t)
		fromClient, err := client.DeleteFrom(users.QueryTable()).WhereEqual(id, 42).Build()
		require.NoError(t, err)
		typed, err := rasql.DeleteFrom(client, users).WhereEqual(id, 42).Build()
		require.NoError(t, err)
		require.Equal(t, typed.SQL(), fromClient.SQL())
	})
}

// clientForBuild returns a client that renders statements without executing them.
func clientForBuild(t *testing.T) rasql.Client {
	t.Helper()

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
	})
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return client
}
