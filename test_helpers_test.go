package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

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
