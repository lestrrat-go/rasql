//go:build unix

package catalog_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// --- Live PostgreSQL and MySQL tests -------------------------------------
//
// Phase 4 specification §7 "PR 3": what a real PostgreSQL or MySQL server
// settles that no fixture test, and no SQLite round trip, can. The single
// strongest reason this file exists is the named foreign key case: SQLite
// reads foreign keys from PRAGMA foreign_key_list, which reports no
// constraint name, so a SQLite foreign key always comes back unnamed and
// the exact line the old comparison reduced to a bare
// `~ foreign key "fk"` (§1.1) cannot be produced without a live server.
//
// mustExecLive is defined in catalog_integration_test.go, in this same
// package, and is reused here rather than redeclared.

// TestDriftIsEmptyForAnUnchangedLivePostgreSQLTable settles, in CI, open
// question §8.2: whether two reads of an unchanged live table are
// representationally identical. It creates a table with a primary key, a
// nullable column, a named unique constraint, a check, an index, and a
// foreign key, reads it twice with catalog.FromDatabase, and requires Drift
// to report nothing. If this ever fails, §8.2 says the fix is not to sort
// during normalization -- that would hide a real reorder -- but to find
// which list moved and why.
func TestDriftIsEmptyForAnUnchangedLivePostgreSQLTable(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE orgs (id integer PRIMARY KEY)")
	mustExecLive(t, ctx, database, `CREATE TABLE users (
		id integer PRIMARY KEY,
		email text,
		org_id integer,
		CONSTRAINT users_email_key UNIQUE (email),
		CONSTRAINT users_email_check CHECK (email <> ''),
		CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id)
	)`)
	mustExecLive(t, ctx, database, "CREATE INDEX users_org_id_idx ON users (org_id)")

	options := catalog.Options{Dialect: dialect.PostgreSQL()}
	first, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)
	second, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(first, second)
	require.True(t, report.Empty(), "two reads of an unchanged live PostgreSQL table must report no drift; report:\n%s", report.String())
}

// TestDriftIsEmptyForAnUnchangedLiveMySQLTable is
// TestDriftIsEmptyForAnUnchangedLivePostgreSQLTable's MySQL twin.
func TestDriftIsEmptyForAnUnchangedLiveMySQLTable(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE orgs (id INT PRIMARY KEY)")
	mustExecLive(t, ctx, database, `CREATE TABLE users (
		id INT PRIMARY KEY,
		email VARCHAR(255),
		org_id INT,
		CONSTRAINT users_email_key UNIQUE (email),
		CONSTRAINT users_email_check CHECK (email <> ''),
		CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id)
	)`)
	mustExecLive(t, ctx, database, "CREATE INDEX users_org_id_idx ON users (org_id)")

	options := catalog.Options{Dialect: dialect.MySQL()}
	first, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)
	second, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(first, second)
	require.True(t, report.Empty(), "two reads of an unchanged live MySQL table must report no drift; report:\n%s", report.String())
}

// TestDriftIsEmptyWhenAPrimaryKeyIsAbsentLivePostgreSQL settles, in CI,
// open question §8.1: whether PostgreSQL returns TableDef.PrimaryKey as a
// non-nil empty slice for a table with no primary key, the way
// inspect/inspect.go:2467 is inferred (not observed, from this machine) to
// build it. It creates a table with no primary key at all, requires the
// live descriptor's own PrimaryKey to be non-nil and empty, then requires
// Drift against a copy whose PrimaryKey was set to nil to report nothing --
// which is normalize's rule 3 (§2.4) doing its job.
func TestDriftIsEmptyWhenAPrimaryKeyIsAbsentLivePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE events (id integer NOT NULL, occurred_at timestamp NOT NULL)")

	live, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.NotNil(t, live[0].PrimaryKey, "a table with no primary key must inspect to a non-nil empty PrimaryKey, not nil")
	require.Empty(t, live[0].PrimaryKey)

	described := live[0].Clone()
	described.PrimaryKey = nil

	report := catalog.Drift([]schema.TableDef{described}, live)
	require.True(t, report.Empty(), "normalization must treat a nil PrimaryKey and a non-nil empty one as equal; report:\n%s", report.String())
}

// TestDriftIsEmptyWhenAPrimaryKeyIsAbsentLiveMySQL is
// TestDriftIsEmptyWhenAPrimaryKeyIsAbsentLivePostgreSQL's MySQL twin.
func TestDriftIsEmptyWhenAPrimaryKeyIsAbsentLiveMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE events (id INT NOT NULL, occurred_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)")

	live, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.MySQL()})
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.NotNil(t, live[0].PrimaryKey, "a table with no primary key must inspect to a non-nil empty PrimaryKey, not nil")
	require.Empty(t, live[0].PrimaryKey)

	described := live[0].Clone()
	described.PrimaryKey = nil

	report := catalog.Drift([]schema.TableDef{described}, live)
	require.True(t, report.Empty(), "normalization must treat a nil PrimaryKey and a non-nil empty one as equal; report:\n%s", report.String())
}

// TestDriftReportsAForeignKeyActionChangeLivePostgreSQL is the case §1.1
// and §7 both name as the reason PR 3 exists at all: a named foreign key
// whose ON DELETE action changed. The old comparison reduced this to a
// bare `~ foreign key "fk"` with no detail, and SQLite cannot reproduce it
// because PRAGMA foreign_key_list never reports a constraint name -- only a
// live PostgreSQL or MySQL server names the foreign key it drops and
// re-adds here. The test also asserts the descriptor's own
// ForeignKeys[0].Name is non-empty before the change, so it fails loudly
// rather than silently degrading into the unnamed path if this server ever
// stopped reporting the name.
func TestDriftReportsAForeignKeyActionChangeLivePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE orgs (id integer PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, org_id integer, CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id) ON DELETE CASCADE)")

	options := catalog.Options{Dialect: dialect.PostgreSQL()}
	before, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	usersBefore := requireTable(t, before, "users")
	require.Len(t, usersBefore.ForeignKeys, 1)
	require.NotEmpty(t, usersBefore.ForeignKeys[0].Name,
		"the foreign key must inspect with a name, or this test would silently degrade into the unnamed path §1.1 exists to distinguish from")
	require.Equal(t, schema.Cascade, usersBefore.ForeignKeys[0].OnDelete)

	mustExecLive(t, ctx, database, "ALTER TABLE users DROP CONSTRAINT users_org_id_fkey")
	mustExecLive(t, ctx, database, "ALTER TABLE users ADD CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id) ON DELETE SET NULL")

	after, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.Len(t, report.Changed(), 1)
	drift := report.Changed()[0]
	require.Equal(t, "users", drift.Name())
	changes := drift.Changes()
	require.Len(t, changes, 1)
	require.Equal(t, catalog.ChangeModified, changes[0].Kind)
	require.Equal(t, `foreign key "users_org_id_fkey"`, changes[0].Subject)
	require.Equal(t, []catalog.FieldChange{{Path: "OnDelete", Was: `"CASCADE"`, Now: `"SET NULL"`}}, changes[0].Fields)
}

// TestDriftReportsAForeignKeyActionChangeLiveMySQL is
// TestDriftReportsAForeignKeyActionChangeLivePostgreSQL's MySQL twin, using
// MySQL's own ALTER TABLE ... DROP FOREIGN KEY / ADD CONSTRAINT syntax.
func TestDriftReportsAForeignKeyActionChangeLiveMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE orgs (id INT PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE TABLE users (id INT PRIMARY KEY, org_id INT, CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id) ON DELETE CASCADE)")

	options := catalog.Options{Dialect: dialect.MySQL()}
	before, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	usersBefore := requireTable(t, before, "users")
	require.Len(t, usersBefore.ForeignKeys, 1)
	require.NotEmpty(t, usersBefore.ForeignKeys[0].Name,
		"the foreign key must inspect with a name, or this test would silently degrade into the unnamed path §1.1 exists to distinguish from")
	require.Equal(t, schema.Cascade, usersBefore.ForeignKeys[0].OnDelete)

	mustExecLive(t, ctx, database, "ALTER TABLE users DROP FOREIGN KEY users_org_id_fkey")
	mustExecLive(t, ctx, database, "ALTER TABLE users ADD CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id) ON DELETE SET NULL")

	after, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.Len(t, report.Changed(), 1)
	drift := report.Changed()[0]
	require.Equal(t, "users", drift.Name())
	changes := drift.Changes()
	require.Len(t, changes, 1)
	require.Equal(t, catalog.ChangeModified, changes[0].Kind)
	require.Equal(t, `foreign key "users_org_id_fkey"`, changes[0].Subject)
	require.Equal(t, []catalog.FieldChange{{Path: "OnDelete", Was: `"CASCADE"`, Now: `"SET NULL"`}}, changes[0].Fields)
}

// TestDriftReportsAColumnNullabilityChangeLivePostgreSQL pins a plain,
// unremarkable engine fact -- a column's own NOT NULL flipping -- against a
// live server, rather than trusting only the fixture in drift_test.go that
// asserts rasql's own output back to itself.
func TestDriftReportsAColumnNullabilityChangeLivePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL)")

	options := catalog.Options{Dialect: dialect.PostgreSQL()}
	before, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	mustExecLive(t, ctx, database, "ALTER TABLE users ALTER COLUMN email DROP NOT NULL")

	after, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.Len(t, report.Changed(), 1)
	changes := report.Changed()[0].Changes()
	require.Len(t, changes, 1)
	require.Equal(t, catalog.ChangeModified, changes[0].Kind)
	require.Equal(t, `column "email"`, changes[0].Subject)
	require.Equal(t, []catalog.FieldChange{{Path: "Nullable", Was: "false", Now: "true"}}, changes[0].Fields)
}

// TestDriftReportsAColumnNullabilityChangeLiveMySQL is
// TestDriftReportsAColumnNullabilityChangeLivePostgreSQL's MySQL twin,
// using MODIFY to flip NOT NULL the way MySQL requires -- MySQL has no
// ALTER COLUMN ... DROP NOT NULL form.
func TestDriftReportsAColumnNullabilityChangeLiveMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id INT PRIMARY KEY, email VARCHAR(255) NOT NULL)")

	options := catalog.Options{Dialect: dialect.MySQL()}
	before, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	mustExecLive(t, ctx, database, "ALTER TABLE users MODIFY email VARCHAR(255) NULL")

	after, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.Len(t, report.Changed(), 1)
	changes := report.Changed()[0].Changes()
	require.Len(t, changes, 1)
	require.Equal(t, catalog.ChangeModified, changes[0].Kind)
	require.Equal(t, `column "email"`, changes[0].Subject)
	require.Equal(t, []catalog.FieldChange{{Path: "Nullable", Was: "false", Now: "true"}}, changes[0].Fields)
}

// TestDriftReportsMySQLIntegerFacts pins an engine fact only MySQL has:
// unsigned, display width, and ZEROFILL, none of which any hand-written
// column comparison in the old code ever checked (§1.1). A plain INT
// column is altered to INT(11) UNSIGNED ZEROFILL and the report must name
// every fact that moved.
//
// MySQL 8.0.19+ deprecates the integer display width and may no longer
// report one back for a plain column, though it is documented to still
// matter, and still be reported, for a column carrying ZEROFILL --
// inspect/integer_display_width_integration_test.go pins the identical
// caveat directly against inspect. This reads the live catalog's own
// COLUMN_TYPE first, rather than assume a width this server's version may
// not state, so the assertion matches whatever server is actually
// connected instead of a guess that could stop passing on a version bump.
func TestDriftReportsMySQLIntegerFacts(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE counters (id INT PRIMARY KEY, total INT NOT NULL)")

	options := catalog.Options{Dialect: dialect.MySQL()}
	before, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	mustExecLive(t, ctx, database, "ALTER TABLE counters MODIFY total INT(11) UNSIGNED ZEROFILL NOT NULL")

	var columnType string
	err = database.QueryRowContext(ctx,
		"SELECT column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
		"counters", "total",
	).Scan(&columnType)
	require.NoError(t, err, "read live COLUMN_TYPE")

	after, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.Len(t, report.Changed(), 1)
	changes := report.Changed()[0].Changes()
	require.Len(t, changes, 1)
	require.Equal(t, catalog.ChangeModified, changes[0].Kind)
	require.Equal(t, `column "total"`, changes[0].Subject)

	want := []catalog.FieldChange{{Path: "Type.Unsigned", Was: "false", Now: "true"}}
	if strings.Contains(columnType, "(11)") {
		want = append(want, catalog.FieldChange{Path: "Type.DisplayWidth", Was: "{}", Now: "{value:11 set:true}"})
	}
	want = append(want, catalog.FieldChange{Path: "Type.ZeroFill", Was: "false", Now: "true"})
	require.Equal(t, want, changes[0].Fields,
		"live COLUMN_TYPE was %q", columnType)
}

// TestDriftReportsPostgreSQLConstraintDeferrabilityLive pins a named unique
// constraint's Deferrable fact against a real PostgreSQL server. PostgreSQL
// has no ALTER to flip a constraint's deferrability in place, so the
// constraint is dropped and re-added under the same name with DEFERRABLE
// INITIALLY DEFERRED.
func TestDriftReportsPostgreSQLConstraintDeferrabilityLive(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE members (id integer PRIMARY KEY, email text NOT NULL, CONSTRAINT members_email_key UNIQUE (email))")

	options := catalog.Options{Dialect: dialect.PostgreSQL()}
	before, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	mustExecLive(t, ctx, database, "ALTER TABLE members DROP CONSTRAINT members_email_key")
	mustExecLive(t, ctx, database, "ALTER TABLE members ADD CONSTRAINT members_email_key UNIQUE (email) DEFERRABLE INITIALLY DEFERRED")

	after, err := catalog.FromDatabase(ctx, database, options)
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.Len(t, report.Changed(), 1)
	changes := report.Changed()[0].Changes()
	require.Len(t, changes, 1)
	require.Equal(t, catalog.ChangeModified, changes[0].Kind)
	require.Equal(t, `unique constraint "members_email_key"`, changes[0].Subject)
	require.Equal(t, []catalog.FieldChange{{Path: "Deferrable", Was: `""`, Now: `"DEFERRABLE INITIALLY DEFERRED"`}}, changes[0].Fields)
}

// requireTable returns the table named name from tables, failing the test
// if it is not present.
func requireTable(t *testing.T, tables []schema.TableDef, name string) schema.TableDef {
	t.Helper()
	for _, table := range tables {
		if table.Name == name {
			return table
		}
	}
	require.Failf(t, "table not found", "%q not found among %d tables", name, len(tables))
	return schema.TableDef{}
}
