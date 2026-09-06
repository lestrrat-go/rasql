//go:build unix

package postgresql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate/diff"
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
	require.Contains(t, plan.Statements[0].SQL, "aaa_child")
	require.Contains(t, plan.Statements[1].SQL, "zzz_owner")

	database := dbtest.PostgreSQLDB(t)
	postgresExec(t, ctx, database, "CREATE TABLE existing_table (id bigint PRIMARY KEY)")
	err = executeFirstPostgreSQLPlanError(ctx, database, plan)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "42P01", pgErr.Code)
	require.Contains(t, err.Error(), "zzz_owner")
	require.NoError(t, database.QueryRowContext(ctx, "SELECT to_regclass('aaa_child')").Scan(new(sql.NullString)))
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
	ctx := t.Context()
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE existing_table (id bigint PRIMARY KEY); CREATE TABLE aaa_child (id bigint PRIMARY KEY, owner_id bigint REFERENCES zzz_owner(id)); CREATE TABLE zzz_owner (id bigint PRIMARY KEY, child_id bigint REFERENCES aaa_child(id));")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	database := dbtest.PostgreSQLDB(t)
	postgresExec(t, ctx, database, "CREATE TABLE existing_table (id bigint PRIMARY KEY)")
	err = executeFirstPostgreSQLPlanError(ctx, database, plan)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "42P01", pgErr.Code)
	require.NoError(t, database.QueryRowContext(ctx, "SELECT to_regclass('aaa_child'), to_regclass('zzz_owner')").Scan(new(sql.NullString), new(sql.NullString)))
}

func executeFirstPostgreSQLPlanError(ctx context.Context, database *sql.DB, plan diff.Plan) error {
	for _, statement := range plan.Statements {
		if _, err := database.ExecContext(ctx, string(statement.SQL)); err != nil {
			return err
		}
	}
	return errors.New("plan unexpectedly applied")
}

func postgresExec(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err)
}
