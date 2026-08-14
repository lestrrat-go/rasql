package postgresql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestDiffLiveMatchesInlinePrimaryKey(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	liveSources, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	live := parseSources(t, analyzer, liveSources)

	plan, err := analyzer.Diff(baseline, live)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

// TestLiveSourcesRejectsNonDefaultIndexMethod proves that an inspected table
// carrying a non-default index method, such as a GIN index, does not reach
// diff-live's generated desired-schema sources as a silently downgraded
// plain index: LiveSources renders through render.CreateIndexes, which
// refuses the method, so the error surfaces here rather than a Plan going on
// to emit the wrong DDL for it.
func TestLiveSourcesRejectsNonDefaultIndexMethod(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tags", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "members_tags_gin_idx",
			Columns: []string{"tags"},
			Method:  "gin",
		}},
	})
	require.ErrorContains(t, err, `"members_tags_gin_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

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

func TestDiffGeneratesNewTable(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{{
		Source:  "001_create_table_projects.sql",
		SQL:     "CREATE TABLE projects (id bigint PRIMARY KEY, owner_id bigint NOT NULL);\n",
		Summary: "create table projects",
	}}, plan.Statements)
}

func TestDiffRejectsCollidingGeneratedStatementNames(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `CREATE TABLE members (id bigint PRIMARY KEY); CREATE TABLE "foo-bar" (id bigint PRIMARY KEY); CREATE TABLE foo_bar (id bigint PRIMARY KEY);`)

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, `duplicate generated SQL source "create_table_foo_bar.sql"`)
	require.ErrorContains(t, err, "create table foo-bar")
	require.ErrorContains(t, err, "create table foo_bar")
	require.NotContains(t, err.Error(), "001_")
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
	require.Equal(t, []diff.PlannedStatement{{
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

func TestDiffTreatsQuotedLowercaseIdentifiersAsEquivalent(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE members ("members" text);`)
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (members text);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffDistinguishesMixedCaseIdentifiers(t *testing.T) {
	analyzer := postgresql.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE members ("Members" text);`)
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (Members text);")

	_, err := analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "column members.Members was removed")
}

func TestParseRejectsUnsupportedDesiredSchemaStatement(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "views.sql", SQL: "CREATE VIEW member_names AS SELECT name FROM members;"}})
	require.ErrorContains(t, err, "must be CREATE TABLE or named CREATE INDEX")
}

func TestParseRejectsIndexForMissingTable(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE INDEX orphan_idx ON missing (id);
	`}})
	require.ErrorContains(t, err, `postgresql schema source "indexes.sql"`)
	require.ErrorContains(t, err, "missing table missing")
}

func TestParseRejectsIndexOnlySourceForMissingTable(t *testing.T) {
	analyzer := postgresql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: "CREATE INDEX orphan_idx ON missing (id);"}})
	require.EqualError(t, err, `postgresql schema source "indexes.sql" defines index orphan_idx on missing table missing`)
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

func parseSources(t *testing.T, analyzer postgresql.Analyzer, sources []diff.Source) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse(sources)
	require.NoError(t, err)
	return snapshot
}
