package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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
		Statements: []diff.Statement{
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
	require.Equal(t, []diff.Statement{{
		Source:  "001_create_table_projects.sql",
		SQL:     "CREATE TABLE projects (id integer PRIMARY KEY, owner_id integer NOT NULL);\n",
		Summary: "create table projects",
	}}, plan.Statements)
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

func parseSnapshot(t *testing.T, analyzer sqlite.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.NoError(t, err)
	return snapshot
}
