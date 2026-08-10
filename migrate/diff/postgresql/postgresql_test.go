package postgresql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/stretchr/testify/require"
)

func TestDiffGeneratesAdditiveColumnsAndIndexes(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id bigint PRIMARY KEY,
			name text NOT NULL
		);
		CREATE INDEX members_name_idx ON members (name);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (
			id bigint PRIMARY KEY,
			name text NOT NULL,
			email text
		);
		CREATE INDEX members_name_idx ON members (name);
		CREATE INDEX members_email_idx ON members (email);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, diff.Plan{
		Dialect: "postgresql",
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

func TestDiffGeneratesNewTable(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.Statement{{
		Source:  "001_create_table_projects.sql",
		SQL:     "CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);\n",
		Summary: "create table projects",
	}}, plan.Statements)
}

func TestDiffRejectsNewRequiredColumnWithoutBackfill(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text NOT NULL);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "new required column members.email needs an application-specific backfill")
}

func TestDiffGeneratesNewRequiredColumnWithDefault(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, active boolean NOT NULL DEFAULT true);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.Statement{{
		Source:  "001_add_column_members_active.sql",
		SQL:     "ALTER TABLE members ADD COLUMN active boolean NOT NULL DEFAULT TRUE;\n",
		Summary: "add column members.active",
	}}, plan.Statements)
}

func TestDiffRejectsNewRequiredPrimaryKeyColumnWithDefaultWhenPrimaryKeyFollowsNotNull(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, active integer NOT NULL DEFAULT 1 PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "new required column members.active needs an application-specific backfill")
}

func TestDiffRejectsNewRequiredColumnWithNullDefault(t *testing.T) {
	for _, columnDefinition := range []string{
		"email text DEFAULT NULL NOT NULL",
		"email text NOT NULL DEFAULT NULL",
	} {
		t.Run(columnDefinition, func(t *testing.T) {
			analyzer := postgresql.New()
			baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
			target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, "+columnDefinition+");")

			_, err := analyzer.Diff(baseline, target)
			require.ErrorContains(t, err, "new required column members.email needs an application-specific backfill")
		})
	}
}

func TestDiffRejectsRemovedColumns(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "column members.email was removed")
}

func TestParseRejectsUnsupportedDesiredSchemaStatement(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "views.sql", SQL: "CREATE VIEW member_names AS SELECT name FROM members;"}})
	require.ErrorContains(t, err, "must be CREATE TABLE or named CREATE INDEX")
}

func TestDiffRejectsConcurrentIndex(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE INDEX CONCURRENTLY members_id_idx ON members (id);
	`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "uses CONCURRENTLY")
}

func parseSnapshot(t *testing.T, analyzer postgresql.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.NoError(t, err)
	return snapshot
}
