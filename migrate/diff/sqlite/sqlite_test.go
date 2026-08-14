package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestLiveSourcesRejectsStrictTable proves that an inspected table carrying
// Strict does not reach diff-live's generated desired-schema sources as a
// silently downgraded plain, non-STRICT table: LiveSources renders through
// render.CreateTable, which refuses Strict, so the error surfaces here
// rather than a Plan going on to emit the wrong DDL for it.
func TestLiveSourcesRejectsStrictTable(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Strict:     true,
	})
	require.ErrorContains(t, err, `"members"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsWithoutRowIDTable is the WithoutRowID counterpart
// to TestLiveSourcesRejectsStrictTable.
func TestLiveSourcesRejectsWithoutRowIDTable(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:         "members",
		Columns:      []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey:   []string{"id"},
		WithoutRowID: true,
	})
	require.ErrorContains(t, err, `"members"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsPrimaryKeyAutoincrement is the
// PrimaryKeyAutoincrement counterpart to TestLiveSourcesRejectsStrictTable.
func TestLiveSourcesRejectsPrimaryKeyAutoincrement(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:                    "members",
		Columns:                 []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey:              []string{"id"},
		PrimaryKeyAutoincrement: true,
	})
	require.ErrorContains(t, err, `"members"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsPrimaryKeyConflictResolution is the
// PrimaryKeyOnConflict counterpart to TestLiveSourcesRejectsStrictTable.
func TestLiveSourcesRejectsPrimaryKeyConflictResolution(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:                 "members",
		Columns:              []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
		PrimaryKey:           []string{"id"},
		PrimaryKeyOnConflict: schema.ConflictReplace,
	})
	require.ErrorContains(t, err, `"members"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

func TestDiffGeneratesAdditiveColumnsAndIndexes(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id integer PRIMARY KEY,
			name text NOT NULL
		);
		CREATE INDEX members_name_idx ON members (name);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id integer PRIMARY KEY,
			name text NOT NULL,
			email text
		);
		CREATE INDEX members_name_idx ON members (name);
		CREATE INDEX members_email_idx ON members (email);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, diff.Plan{
		Dialect: "sqlite",
		Statements: []diff.PlannedStatement{
			{
				Source:  "001_add_column_members_email.sql",
				SQL:     "ALTER TABLE members ADD COLUMN email text;\n",
				Summary: "add column members.email",
			},
			{
				Source:  "002_create_index_members_email_idx.sql",
				SQL:     "CREATE INDEX members_email_idx ON members (email);\n",
				Summary: "create index members_email_idx",
			},
		},
	}, plan)
}

func TestDiffGeneratedMigrationAppliesSQLite(t *testing.T) {
	analyzer := sqlite.New()
	baselineSource := "CREATE TABLE members (id integer PRIMARY KEY, name text NOT NULL);"
	baseline := parseSnapshot(t, analyzer, baselineSource)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id integer PRIMARY KEY,
			name text NOT NULL,
			active integer NOT NULL DEFAULT 1
		);
	`)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)

	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	_, err = database.ExecContext(t.Context(), baselineSource)
	require.NoError(t, err)
	for _, statement := range plan.Statements {
		_, err = database.ExecContext(t.Context(), statement.SQL)
		require.NoError(t, err)
	}
	rows, err := database.QueryContext(t.Context(), "SELECT active FROM members")
	require.NoError(t, err)
	require.NoError(t, rows.Close())
}

func TestDiffGeneratesNewTable(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id integer PRIMARY KEY);
		CREATE TABLE projects (id integer PRIMARY KEY, owner_id integer NOT NULL);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{{
		Source:  "001_create_table_projects.sql",
		SQL:     "CREATE TABLE projects (id integer PRIMARY KEY, owner_id integer NOT NULL);\n",
		Summary: "create table projects",
	}}, plan.Statements)
}

func TestDiffRejectsCollidingGeneratedStatementNames(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `CREATE TABLE members (id integer PRIMARY KEY); CREATE TABLE "foo-bar" (id integer PRIMARY KEY); CREATE TABLE foo_bar (id integer PRIMARY KEY);`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, `duplicate generated SQL source "create_table_foo_bar.sql"`)
	require.ErrorContains(t, err, "create table foo-bar")
	require.ErrorContains(t, err, "create table foo_bar")
	require.NotContains(t, err.Error(), "001_")
}

func TestDiffPreservesSQLiteForeignKeyActions(t *testing.T) {
	analyzer := sqlite.New()
	schema := `
		CREATE TABLE parents (id integer PRIMARY KEY);
		CREATE TABLE children (
			parent_id integer REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE
		);
	`
	baseline := parseSnapshot(t, analyzer, schema)
	target := parseSnapshot(t, analyzer, schema)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffIgnoresSQLiteForeignKeyConstraintOrder(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE parent_a (id integer PRIMARY KEY);
		CREATE TABLE parent_b (id integer PRIMARY KEY);
		CREATE TABLE multi (
			b_id integer REFERENCES parent_b(id) ON UPDATE CASCADE,
			a_id integer REFERENCES parent_a(id) ON DELETE CASCADE
		);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE parent_a (id integer PRIMARY KEY);
		CREATE TABLE parent_b (id integer PRIMARY KEY);
		CREATE TABLE multi (
			a_id integer REFERENCES parent_a(id) ON DELETE CASCADE,
			b_id integer REFERENCES parent_b(id) ON UPDATE CASCADE
		);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffUsesSQLiteCaseInsensitiveIdentifiers(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE Members (ID integer PRIMARY KEY, Email text);
		CREATE INDEX Members_Email_IDX ON Members (Email);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT);
		CREATE INDEX members_email_idx ON members (email);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffCanonicalizesMixedCaseTablesAndUnorderedForeignKeys(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE "ParentA" ("ID" integer PRIMARY KEY);
		CREATE TABLE "ParentB" ("ID" integer PRIMARY KEY);
		CREATE TABLE "MixedCase" (
			"B_ID" integer REFERENCES "ParentB"("ID") ON UPDATE CASCADE,
			"A_ID" integer REFERENCES "ParentA"("ID") ON DELETE CASCADE
		);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE "parenta" ("id" INTEGER PRIMARY KEY);
		CREATE TABLE "parentb" ("id" INTEGER PRIMARY KEY);
		CREATE TABLE "mixedcase" (
			"a_id" INTEGER REFERENCES "parenta"("id") ON DELETE CASCADE,
			"b_id" INTEGER REFERENCES "parentb"("id") ON UPDATE CASCADE
		);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffDetectsChangedSQLiteForeignKeyAction(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE parent_a (id integer PRIMARY KEY);
		CREATE TABLE multi (a_id integer REFERENCES parent_a(id) ON DELETE CASCADE);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE parent_a (id integer PRIMARY KEY);
		CREATE TABLE multi (a_id integer REFERENCES parent_a(id) ON DELETE SET NULL);
	`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "table multi constraints changed")
}

func TestDiffGeneratesNewTableWithSQLiteForeignKeyActions(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE parents (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE parents (id integer PRIMARY KEY);
		CREATE TABLE children (
			parent_id integer,
			FOREIGN KEY (parent_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE
		);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE children (parent_id integer, FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE CASCADE ON UPDATE CASCADE);\n", plan.Statements[0].SQL)
}

func TestDiffRejectsNewRequiredColumnWithoutBackfill(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY, email text NOT NULL);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "new column members.email needs an application-specific backfill")
}

func TestDiffRejectsUnsupportedSQLiteColumnAdditions(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	for _, test := range []struct {
		name     string
		target   string
		expected string
	}{
		{
			name:     "unique",
			target:   "CREATE TABLE members (id integer PRIMARY KEY, email text UNIQUE);",
			expected: "has a UNIQUE constraint that SQLite ALTER TABLE cannot add",
		},
		{
			name:     "nonliteral default",
			target:   "CREATE TABLE members (id integer PRIMARY KEY, created_at text DEFAULT CURRENT_TIMESTAMP);",
			expected: "has a nonliteral default that SQLite ALTER TABLE cannot add",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := parseSnapshot(t, analyzer, test.target)
			_, err := analyzer.Diff(baseline, target)
			require.ErrorContains(t, err, test.expected)
		})
	}
}

func TestDiffRejectsChangedOptions(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY) STRICT;")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "table members options changed")
}

func TestDiffDoesNotNormalizeAwaySQLitePrimaryKeyMetadata(t *testing.T) {
	analyzer := sqlite.New()
	for _, test := range []struct {
		name   string
		target string
	}{
		{name: "autoincrement", target: "CREATE TABLE members (id integer PRIMARY KEY AUTOINCREMENT);"},
		{name: "conflict resolution", target: "CREATE TABLE members (id integer PRIMARY KEY ON CONFLICT REPLACE);"},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
			target := parseSnapshot(t, analyzer, test.target)
			_, err := analyzer.Diff(baseline, target)
			require.ErrorContains(t, err, "table members constraints changed")
		})
	}
}

func TestParseRejectsCreateTableAsSelect(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "member_copy.sql", SQL: "CREATE TABLE member_copy AS SELECT id FROM members;"}})
	require.ErrorContains(t, err, "CREATE TABLE AS SELECT")
}

func TestParseRejectsUnsupportedDesiredSchemaStatement(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "views.sql", SQL: "CREATE VIEW member_names AS SELECT name FROM members;"}})
	require.ErrorContains(t, err, "must be CREATE TABLE or named CREATE INDEX")
}

func TestParseRejectsIndexForMissingTable(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: `
		CREATE TABLE members (id integer PRIMARY KEY);
		CREATE INDEX orphan_idx ON missing (id);
	`}})
	require.ErrorContains(t, err, `sqlite schema source "indexes.sql"`)
	require.ErrorContains(t, err, "missing table missing")
}

func TestParseRejectsIndexOnlySourceForMissingTable(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: "CREATE INDEX orphan_idx ON missing (id);"}})
	require.EqualError(t, err, `sqlite schema source "indexes.sql" defines index orphan_idx on missing table missing`)
}

func TestParseAcceptsQualifiedIndexOwner(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: `
		CREATE TABLE members (id integer PRIMARY KEY);
		CREATE INDEX members_id_idx ON members (id);
		CREATE TABLE "audit"."events" (id integer PRIMARY KEY, user_id integer);
		CREATE INDEX "audit"."events_user_id_idx" ON "events" (user_id);
		CREATE TABLE "archive"."events" (id integer PRIMARY KEY, user_id integer);
		CREATE INDEX "archive"."events_user_id_idx" ON "archive"."events" (user_id);
	`}})
	require.NoError(t, err)
}

func parseSnapshot(t *testing.T, analyzer sqlite.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.NoError(t, err)
	return snapshot
}
