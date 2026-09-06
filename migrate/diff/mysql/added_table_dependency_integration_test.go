//go:build unix

package mysql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/stretchr/testify/require"
)

func TestAddedTableDependencyMySQL(t *testing.T) {
	ctx := t.Context()
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY); CREATE TABLE zzz_owner (id BIGINT PRIMARY KEY); CREATE TABLE aaa_child (id BIGINT PRIMARY KEY, owner_id BIGINT NOT NULL, FOREIGN KEY (owner_id) REFERENCES zzz_owner(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	require.Contains(t, plan.Statements[0].SQL, "aaa_child")
	require.Contains(t, plan.Statements[1].SQL, "zzz_owner")

	database := dbtest.MySQLDB(t)
	mysqlExec(t, ctx, database, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY)")
	err = executeFirstMySQLPlanError(ctx, database, plan)
	var mysqlErr *gomysql.MySQLError
	require.ErrorAs(t, err, &mysqlErr)
	require.Equal(t, uint16(1824), mysqlErr.Number)
	require.Contains(t, err.Error(), "zzz_owner")
	var childExists int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'aaa_child'").Scan(&childExists))
	require.Equal(t, 0, childExists)
}

func TestAddedTableDependencyMySQLSelfReference(t *testing.T) {
	ctx := t.Context()
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY); CREATE TABLE self_ref (id BIGINT PRIMARY KEY, parent_id BIGINT, FOREIGN KEY (parent_id) REFERENCES self_ref(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 1)
	database := dbtest.MySQLDB(t)
	mysqlExec(t, ctx, database, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY)")
	mysqlExec(t, ctx, database, string(plan.Statements[0].SQL))
	var exists int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'self_ref'").Scan(&exists))
	require.Equal(t, 1, exists)
}

func TestAddedTableDependencyMySQLCycle(t *testing.T) {
	ctx := t.Context()
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY); CREATE TABLE aaa_child (id BIGINT PRIMARY KEY, owner_id BIGINT, FOREIGN KEY (owner_id) REFERENCES zzz_owner(id)); CREATE TABLE zzz_owner (id BIGINT PRIMARY KEY, child_id BIGINT, FOREIGN KEY (child_id) REFERENCES aaa_child(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	database := dbtest.MySQLDB(t)
	mysqlExec(t, ctx, database, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY)")
	err = executeFirstMySQLPlanError(ctx, database, plan)
	var mysqlErr *gomysql.MySQLError
	require.ErrorAs(t, err, &mysqlErr)
	require.Equal(t, uint16(1824), mysqlErr.Number)
	var count int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('aaa_child', 'zzz_owner')").Scan(&count))
	require.Equal(t, 0, count)
}

func executeFirstMySQLPlanError(ctx context.Context, database *sql.DB, plan diff.Plan) error {
	for _, statement := range plan.Statements {
		if _, err := database.ExecContext(ctx, string(statement.SQL)); err != nil {
			return err
		}
	}
	return errors.New("plan unexpectedly applied")
}

func mysqlExec(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err)
}
