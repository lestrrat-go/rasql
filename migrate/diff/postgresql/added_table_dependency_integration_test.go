//go:build unix

package postgresql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/stretchr/testify/require"
)

func TestAddedTableDependencyPostgreSQL(t *testing.T) {
	ctx := t.Context()
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY); CREATE TABLE zzz_owner (id bigint PRIMARY KEY); CREATE TABLE aaa_child (id bigint PRIMARY KEY, owner_id bigint NOT NULL REFERENCES zzz_owner(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	require.Contains(t, plan.Statements[0].SQL, "zzz_owner")
	require.Contains(t, plan.Statements[1].SQL, "aaa_child")

	database := dbtest.PostgreSQLDB(t)
	postgresExec(t, ctx, database, "CREATE TABLE existing_table (id bigint PRIMARY KEY)")
	for _, statement := range plan.Statements {
		postgresExec(t, ctx, database, string(statement.SQL))
	}
	var exists sql.NullString
	require.NoError(t, database.QueryRowContext(ctx, "SELECT to_regclass('aaa_child')").Scan(&exists))
	require.True(t, exists.Valid)
}

func TestAddedTableDependencyPostgreSQLSelfReference(t *testing.T) {
	ctx := t.Context()
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY); CREATE TABLE self_ref (id bigint PRIMARY KEY, parent_id bigint REFERENCES self_ref(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 1)
	database := dbtest.PostgreSQLDB(t)
	postgresExec(t, ctx, database, "CREATE TABLE existing_table (id bigint PRIMARY KEY)")
	postgresExec(t, ctx, database, string(plan.Statements[0].SQL))
	var exists sql.NullString
	require.NoError(t, database.QueryRowContext(ctx, "SELECT to_regclass('self_ref')").Scan(&exists))
	require.True(t, exists.Valid)
}

func TestAddedTableDependencyPostgreSQLCycle(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY); CREATE TABLE aaa_child (id bigint PRIMARY KEY, owner_id bigint REFERENCES zzz_owner(id)); CREATE TABLE zzz_owner (id bigint PRIMARY KEY, child_id bigint REFERENCES aaa_child(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "manual migration")
	require.ErrorContains(t, err, "aaa_child")
	require.ErrorContains(t, err, "zzz_owner")
	require.Empty(t, plan.Statements)
}

func postgresExec(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err)
}
