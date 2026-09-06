//go:build unix

package mysql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
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
	require.Contains(t, plan.Statements[0].SQL, "zzz_owner")
	require.Contains(t, plan.Statements[1].SQL, "aaa_child")

	database := dbtest.MySQLDB(t)
	mysqlExec(t, ctx, database, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY)")
	for _, statement := range plan.Statements {
		mysqlExec(t, ctx, database, string(statement.SQL))
	}
	var childExists int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'aaa_child'").Scan(&childExists))
	require.Equal(t, 1, childExists)
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
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id BIGINT PRIMARY KEY); CREATE TABLE aaa_child (id BIGINT PRIMARY KEY, owner_id BIGINT, FOREIGN KEY (owner_id) REFERENCES zzz_owner(id)); CREATE TABLE zzz_owner (id BIGINT PRIMARY KEY, child_id BIGINT, FOREIGN KEY (child_id) REFERENCES aaa_child(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "manual migration")
	require.ErrorContains(t, err, "aaa_child")
	require.ErrorContains(t, err, "zzz_owner")
	require.Empty(t, plan.Statements)
}

func mysqlExec(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err)
}
