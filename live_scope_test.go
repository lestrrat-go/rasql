//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestLiveSavepointScope(t *testing.T) {
	t.Run("PostgreSQL", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		testLiveSavepoint(t, database, dialect.PostgreSQL(), "postgresql-17")
	})

	t.Run("MySQL", func(t *testing.T) {
		database := dbtest.MySQLDB(t)
		testLiveSavepoint(t, database, dialect.MySQL(), "mysql-8.4")
	})
}

func testLiveSavepoint(t *testing.T, database *sql.DB, d dialect.Dialect, profileID string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), "CREATE TABLE values_table (value INTEGER)")
	require.NoError(t, err)
	db, err := rasql.New(database, d)
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion(profileID, 17, 11, 0)
	if profileID == "mysql-8.4" {
		profile, err = rasql.EngineProfileFromVersion(profileID, 8, 4, 11)
	}
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	sentinel := errors.New("savepoint callback failed")
	require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, outer rasql.Executor) error {
		_, err := outer.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (1)"))
		require.NoError(t, err)
		err = rasql.Within(ctx, outer, nil, func(ctx context.Context, nested rasql.Executor) error {
			_, err := nested.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (2)"))
			require.NoError(t, err)
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)
		_, err = outer.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (3)"))
		return err
	}))
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM values_table").Scan(&count))
	require.Equal(t, 2, count)
}
