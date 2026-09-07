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
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestAtomicLiveEnginesPreserveOuterWrites(t *testing.T) {
	engines := []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
	}{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
	}
	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			database := engine.open(t)
			db, err := rasql.New(database, engine.dialect)
			require.NoError(t, err)
			table := dbtest.UniqueName(t, "rasql_atomic_scope_records")
			quoted, err := engine.dialect.QuoteIdentifier(table)
			require.NoError(t, err)
			_, err = db.ExecRendered(t.Context(), stmt.New(sqltext.Text("CREATE TABLE "+quoted+" (value BIGINT)")))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = db.ExecRendered(context.Background(), stmt.New(sqltext.Text("DROP TABLE "+quoted)))
			})

			callbackErr := errors.New("nested live failure")
			err = db.Atomic(t.Context(), nil, func(ctx context.Context, outer rasql.DB) error {
				_, err := outer.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO "+quoted+" VALUES (1)")))
				require.NoError(t, err)
				err = outer.Atomic(ctx, nil, func(ctx context.Context, nested rasql.DB) error {
					_, err := nested.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO "+quoted+" VALUES (2)")))
					require.NoError(t, err)
					return callbackErr
				})
				require.ErrorIs(t, err, callbackErr)
				_, err = outer.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO "+quoted+" VALUES (3)")))
				return err
			})
			require.NoError(t, err)
			var count, total int64
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*), COALESCE(SUM(value), 0) FROM "+quoted).Scan(&count, &total))
			require.Equal(t, int64(2), count)
			require.Equal(t, int64(4), total)
		})
	}
}
