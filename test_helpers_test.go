package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type deleteUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

func deleteUsersTable(t *testing.T) rasql.Table[deleteUser] {
	t.Helper()

	users, err := rasql.TableOf[deleteUser](schema.TableDef{
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

// dbForBuild returns a DB that renders statements without executing them.
func dbForBuild(t *testing.T) rasql.DB {
	t.Helper()

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return db
}
