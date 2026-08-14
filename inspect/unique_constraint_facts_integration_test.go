//go:build unix

package inspect_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// postgreSQLServerVersion returns the connected server's own reported
// server_version_num, the same catalog value inspect itself reads to pick
// a query variant for the running server. Tests use it to skip an
// assertion that needs a newer server than the one actually reachable,
// such as NULLS NOT DISTINCT needing PostgreSQL 15 or later.
func postgreSQLServerVersion(t *testing.T, ctx context.Context, database *sql.DB) int {
	t.Helper()
	var raw string
	require.NoError(t, database.QueryRowContext(ctx, "SHOW server_version_num").Scan(&raw))
	version, err := strconv.Atoi(raw)
	require.NoError(t, err)
	return version
}

// TestPostgreSQLInspectorRecordsDeferrableUniqueConstraintAgainstLiveDatabase
// pins what a real PostgreSQL server reports back for a DEFERRABLE
// INITIALLY DEFERRED unique constraint, because
// TestPostgreSQLInspectorRecordsUniqueConstraintFacts in inspect_test.go
// only asserts rasql's own mocked catalog echoes what the test told it to.
// Before this feature existed, inspecting this table failed outright: the
// deferrable unique constraint made the whole table unrepresentable, which
// is the exact failure a sweep over a production schema must not hit on
// its first deferrable unique constraint.
func TestPostgreSQLInspectorRecordsDeferrableUniqueConstraintAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE members (id integer PRIMARY KEY, email text NOT NULL, CONSTRAINT members_email_key UNIQUE (email) DEFERRABLE INITIALLY DEFERRED)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "members")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "members_email_key", Columns: []string{"email"}, Deferrable: schema.DeferrableInitiallyDeferred},
	}, table.UniqueConstraints)
}

// TestPostgreSQLInspectorRecordsNullsNotDistinctUniqueConstraintAgainstLiveDatabase
// pins what a real PostgreSQL 15+ server reports back for a UNIQUE NULLS
// NOT DISTINCT constraint. NULLS NOT DISTINCT needs PostgreSQL 15 or
// later, so this test checks the connected server's own reported version
// and skips cleanly on an older one rather than asserting against a clause
// the server would reject.
func TestPostgreSQLInspectorRecordsNullsNotDistinctUniqueConstraintAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	if postgreSQLServerVersion(t, ctx, database) < 150000 {
		t.Skip("UNIQUE NULLS NOT DISTINCT needs PostgreSQL 15 or later")
	}

	mustExec(t, ctx, database, "CREATE TABLE members (id integer PRIMARY KEY, email text, CONSTRAINT members_email_key UNIQUE NULLS NOT DISTINCT (email))")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "members")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "members_email_key", Columns: []string{"email"}, NullsNotDistinct: true},
	}, table.UniqueConstraints)
}

// TestPostgreSQLInspectorRecordsUniqueConstraintIncludeColumnsAgainstLiveDatabase
// pins what a real PostgreSQL server reports back for a unique constraint
// carrying an INCLUDE clause. tags is jsonb rather than a PostgreSQL array
// column: rasql has no column type for ARRAY, and inspecting an array
// column fails before inspection ever reaches the constraint, which is
// unrelated to what this test pins.
func TestPostgreSQLInspectorRecordsUniqueConstraintIncludeColumnsAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE members (id integer PRIMARY KEY, email text NOT NULL, tags jsonb NOT NULL, CONSTRAINT members_email_key UNIQUE (email) INCLUDE (tags))")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "members")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "members_email_key", Columns: []string{"email"}, IncludeColumns: []string{"tags"}},
	}, table.UniqueConstraints)
}
