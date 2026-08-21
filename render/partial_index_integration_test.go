//go:build unix

package render_test

import (
	"context"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestCreateIndexesRendersPartialIndexAgainstLivePostgreSQL proves that
// render.CreateIndexes' own WHERE clause output is DDL a real PostgreSQL
// server accepts, and that the catalog's own
// pg_get_expr(indpred, indrelid) for the new index equals the predicate
// that was rendered -- a fixture test only asserts the rendered SQL
// string, never that a server accepts it or stores the predicate rasql
// expects.
func TestCreateIndexesRendersPartialIndexAgainstLivePostgreSQL(t *testing.T) {
	ctx := context.Background()
	database := dbtest.PostgreSQLDB(t)

	_, err := database.ExecContext(ctx, "CREATE TABLE live_render_tasks (id BIGINT PRIMARY KEY, is_open BOOLEAN NOT NULL)")
	require.NoError(t, err, "create live table")

	table := schema.TableDef{
		Name:       "live_render_tasks",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "is_open", Type: schema.BooleanType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "live_render_tasks_open_idx",
			Columns:   []string{"id"},
			Predicate: "is_open",
		}},
	}

	indexes, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.NoError(t, err)
	require.Len(t, indexes, 1)

	_, err = database.ExecContext(ctx, indexes[0].SQL(), indexes[0].Args()...)
	require.NoError(t, err, "PostgreSQL must accept render.CreateIndexes' own WHERE clause output")

	var catalogPredicate string
	err = database.QueryRowContext(ctx,
		"SELECT pg_get_expr(indpred, indrelid) FROM pg_index WHERE indexrelid = 'live_render_tasks_open_idx'::regclass").
		Scan(&catalogPredicate)
	require.NoError(t, err, "read the new index's own predicate back from the catalog")
	require.Equal(t, "is_open", catalogPredicate, "the catalog's own predicate must equal the text render.CreateIndexes emitted verbatim")
}

// TestCreateIndexRejectsWhereClauseAgainstLiveMySQL pins the MySQL rule
// dialect.CapabilityPartialIndex's absence rests on: a WHERE clause on
// CREATE INDEX fails with MySQL's own syntax error 1064, not merely
// "rasql refuses to build one." The DDL here is issued directly, bypassing
// render, precisely because render already refuses a Predicate on a
// dialect without the capability before it would ever reach the server.
func TestCreateIndexRejectsWhereClauseAgainstLiveMySQL(t *testing.T) {
	ctx := context.Background()
	database := dbtest.MySQLDB(t)

	_, err := database.ExecContext(ctx, "CREATE TABLE live_render_mysql_tasks (id BIGINT PRIMARY KEY, is_open BOOLEAN NOT NULL) ENGINE=InnoDB")
	require.NoError(t, err, "create live table")

	_, err = database.ExecContext(ctx, "CREATE INDEX live_render_mysql_tasks_open_idx ON live_render_mysql_tasks (id) WHERE is_open")
	require.Error(t, err, "MySQL must refuse a WHERE clause on CREATE INDEX")

	var mysqlErr *gomysql.MySQLError
	require.ErrorAs(t, err, &mysqlErr, "the refusal must come from MySQL itself, not from a connection or driver failure")
	require.EqualValues(t, 1064, mysqlErr.Number, "MySQL's own syntax-error number for a WHERE clause on CREATE INDEX")
}
