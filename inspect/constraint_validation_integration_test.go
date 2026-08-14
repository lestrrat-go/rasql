//go:build unix

package inspect_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// postgreSQLServerVersionNum reports the live server's server_version_num,
// the same integer PostgreSQL itself exposes through SHOW
// server_version_num, so a test can skip a fact a server too old to have it
// cannot demonstrate rather than fail on a server this repository does not
// target.
func postgreSQLServerVersionNum(t *testing.T, database *sql.DB) int {
	t.Helper()
	var version int
	require.NoError(t, database.QueryRowContext(t.Context(), "SHOW server_version_num").Scan(&version))
	return version
}

// TestPostgreSQLInspectorRecordsNotValidCheckAgainstLiveDatabase pins what a
// real PostgreSQL server reports back for a NOT VALID check constraint,
// because TestPostgreSQLInspectorRecordsCheckFacts in inspect_test.go only
// asserts rasql's own mocked catalog echoes what the test told it to.
// Before this feature existed, inspecting this table failed outright: the
// NOT VALID check constraint made the whole table unrepresentable, which is
// the exact failure a sweep over a production schema must not hit on its
// first NOT VALID check constraint.
func TestPostgreSQLInspectorRecordsNotValidCheckAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE invoices (id integer PRIMARY KEY, amount integer NOT NULL)")
	mustExec(t, ctx, database, "ALTER TABLE invoices ADD CONSTRAINT invoices_amount_check CHECK (amount >= 0) NOT VALID")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "invoices")
	require.NoError(t, err)
	require.Equal(t, []schema.CheckDef{
		{Name: "invoices_amount_check", Expression: "amount >= 0", NotValid: true},
	}, table.Checks)
}

// TestPostgreSQLInspectorRecordsNotValidForeignKeyAgainstLiveDatabase is the
// foreign-key counterpart to
// TestPostgreSQLInspectorRecordsNotValidCheckAgainstLiveDatabase.
func TestPostgreSQLInspectorRecordsNotValidForeignKeyAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE accounts (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, account_id integer)")
	mustExec(t, ctx, database, "ALTER TABLE users ADD CONSTRAINT users_account_fk FOREIGN KEY (account_id) REFERENCES accounts (id) NOT VALID")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "users")
	require.NoError(t, err)
	require.Len(t, table.ForeignKeys, 1)
	require.Equal(t, "accounts", table.ForeignKeys[0].ReferencedTable)
	require.True(t, table.ForeignKeys[0].NotValid)
}

// TestPostgreSQLInspectorRecordsNotEnforcedCheckAgainstLiveDatabase pins
// what a real PostgreSQL 18+ server reports back for a NOT ENFORCED check
// constraint. NOT ENFORCED is a PostgreSQL 18 feature, and compose.yaml
// currently runs postgres:17-alpine, so this skips cleanly rather than
// failing on a server that cannot express NOT ENFORCED at all.
func TestPostgreSQLInspectorRecordsNotEnforcedCheckAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)
	if version := postgreSQLServerVersionNum(t, database); version < 180000 {
		t.Skipf("NOT ENFORCED check constraints require PostgreSQL 18 or later, server reports %d", version)
	}

	mustExec(t, ctx, database, "CREATE TABLE invoices (id integer PRIMARY KEY, amount integer NOT NULL)")
	mustExec(t, ctx, database, "ALTER TABLE invoices ADD CONSTRAINT invoices_amount_check CHECK (amount >= 0) NOT ENFORCED")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "invoices")
	require.NoError(t, err)
	require.Equal(t, []schema.CheckDef{
		{Name: "invoices_amount_check", Expression: "amount >= 0", NotEnforced: true},
	}, table.Checks)
}
