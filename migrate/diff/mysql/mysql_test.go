package mysql_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/stretchr/testify/require"
)

func TestDiffGeneratesAdditiveColumnsAndIndexes(t *testing.T) {
	analyzer := mysql.New()
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
		Dialect: "mysql",
		Statements: []diff.Statement{
			{
				Source:  "001_add_column_members_email.sql",
				SQL:     "ALTER TABLE members ADD COLUMN email text;\n",
				Summary: "add column members.email",
			},
			{
				Source:  "002_create_index_members_members_email_idx.sql",
				SQL:     "CREATE INDEX members_email_idx ON members (email);\n",
				Summary: "create index members_email_idx",
			},
		},
	}, plan)
}

func TestDiffKeepsSameNamedIndexesDistinctByTable(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE TABLE projects (id bigint PRIMARY KEY, name text);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE TABLE projects (id bigint PRIMARY KEY, name text);
		CREATE INDEX common_idx ON members (email);
		CREATE INDEX common_idx ON projects (name);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, diff.Plan{
		Dialect: "mysql",
		Statements: []diff.Statement{
			{
				Source:  "001_create_index_members_common_idx.sql",
				SQL:     "CREATE INDEX common_idx ON members (email);\n",
				Summary: "create index common_idx",
			},
			{
				Source:  "002_create_index_projects_common_idx.sql",
				SQL:     "CREATE INDEX common_idx ON projects (name);\n",
				Summary: "create index common_idx",
			},
		},
	}, plan)
}

func TestDiffGeneratesNewTable(t *testing.T) {
	analyzer := mysql.New()
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

func TestDiffWritesMigrationForMaximumLengthMultibyteIdentifiers(t *testing.T) {
	tableName := strings.Repeat("表", 64)
	indexName := strings.Repeat("索", 63) + "A"
	otherIndexName := strings.Repeat("索", 63) + "B"
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, fmt.Sprintf("CREATE TABLE `%s` (id bigint);", tableName))
	target := parseSnapshot(t, analyzer, fmt.Sprintf("CREATE TABLE `%s` (id bigint); CREATE INDEX `%s` ON `%s` (id); CREATE INDEX `%s` ON `%s` (id);", tableName, indexName, tableName, otherIndexName, tableName))

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	require.NotEqual(t, plan.Statements[0].Source, plan.Statements[1].Source)
	for _, statement := range plan.Statements {
		require.LessOrEqual(t, len(statement.Source), 255)
	}

	directory := filepath.Join(t.TempDir(), "001_add_index")
	require.NoError(t, diff.WriteMigration(directory, plan))
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestDiffRejectsCollidingGeneratedStatementNames(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY); CREATE TABLE `foo-bar` (id bigint PRIMARY KEY); CREATE TABLE foo_bar (id bigint PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, `duplicate generated SQL source "create_table_foo_bar.sql"`)
	require.ErrorContains(t, err, "create table foo-bar")
	require.ErrorContains(t, err, "create table foo_bar")
	require.NotContains(t, err.Error(), "001_")
}

func TestDiffRejectsNewRequiredColumnWithoutBackfill(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text NOT NULL);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "new required column members.email needs an application-specific backfill")
}

func TestDiffGeneratesNewRequiredColumnWithDefault(t *testing.T) {
	analyzer := mysql.New()
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
	analyzer := mysql.New()
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
			analyzer := mysql.New()
			baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
			target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, "+columnDefinition+");")

			_, err := analyzer.Diff(baseline, target)
			require.ErrorContains(t, err, "new required column members.email needs an application-specific backfill")
		})
	}
}

func TestDiffRejectsRemovedColumns(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY, email text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "column members.email was removed")
}

func TestDiffRejectsChangedIndexes(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE INDEX members_email_idx ON members (email);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE UNIQUE INDEX members_email_idx ON members (email);
	`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "index members_email_idx changed")
}

func TestParseRejectsUnsupportedDesiredSchemaStatement(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "views.sql", SQL: "CREATE VIEW member_names AS SELECT name FROM members;"}})
	require.ErrorContains(t, err, "must be CREATE TABLE or named CREATE INDEX")
}

func parseSnapshot(t *testing.T, analyzer mysql.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.NoError(t, err)
	return snapshot
}
