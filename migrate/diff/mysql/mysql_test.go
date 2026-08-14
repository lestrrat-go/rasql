package mysql_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestLiveSourcesIncludesMySQLOrdinaryIndexes(t *testing.T) {
	analyzer := mysql.New()
	// The indexed column states a width: MySQL refuses to build a key over an
	// unbounded TEXT column (error 1170), so render.CreateIndexes now rejects
	// one here before this test's assertions on the rendered SQL even run.
	sources, err := analyzer.LiveSources(schema.TableDef{
		Name:    "members",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}}},
		Indexes: []schema.IndexDef{{Name: "members_email_idx", Columns: []string{"email"}}},
	})
	require.NoError(t, err)
	require.Len(t, sources, 2)
	require.Contains(t, sources[1].SQL, "CREATE INDEX")
	require.Contains(t, sources[1].SQL, "members_email_idx")
	require.Contains(t, sources[1].SQL, "email")
}

// TestLiveSourcesRejectsIntegerDisplayWidth proves that an inspected table
// carrying a MySQL integer column with a stated display width does not
// reach diff-live's generated desired-schema sources as a silently
// downgraded plain-width column: LiveSources renders through
// render.CreateTable, which refuses a stated DisplayWidth, so the error
// surfaces here rather than a Plan going on to emit DDL that drops it.
func TestLiveSourcesRejectsIntegerDisplayWidth(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(11)}},
		},
		PrimaryKey: []string{"id"},
	})
	require.ErrorContains(t, err, `"total"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsIntegerZeroFill is the ZEROFILL counterpart to
// TestLiveSourcesRejectsIntegerDisplayWidth.
func TestLiveSourcesRejectsIntegerZeroFill(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{Unsigned: true, ZeroFill: true}},
		},
		PrimaryKey: []string{"id"},
	})
	require.ErrorContains(t, err, `"total"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsDecimalUnsigned proves that an inspected table
// carrying a MySQL decimal column with a true Unsigned does not reach
// diff-live's generated desired-schema sources as a silently downgraded
// plain-signed column: LiveSources renders through render.CreateTable,
// which refuses Unsigned, so the error surfaces here rather than a Plan
// going on to emit DDL that drops it.
func TestLiveSourcesRejectsDecimalUnsigned(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true}},
		},
		PrimaryKey: []string{"id"},
	})
	require.ErrorContains(t, err, `"amount"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsDecimalZeroFill is the ZEROFILL counterpart to
// TestLiveSourcesRejectsDecimalUnsigned.
func TestLiveSourcesRejectsDecimalZeroFill(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true}},
		},
		PrimaryKey: []string{"id"},
	})
	require.ErrorContains(t, err, `"amount"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsGeneratedColumn proves that an inspected MySQL
// table carrying a generated column does not reach diff-live's generated
// desired-schema sources as a silently downgraded plain writable column:
// LiveSources renders through render.CreateTable, which refuses
// GeneratedExpression regardless of which engine produced the descriptor,
// so the error surfaces here rather than a Plan going on to emit DDL for a
// column that cannot be written to at all.
func TestLiveSourcesRejectsGeneratedColumn(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.ErrorContains(t, err, `"fahrenheit"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsInvisibleIndex proves that an inspected table
// carrying an invisible index does not reach diff-live's generated
// desired-schema sources as a silently downgraded visible index:
// LiveSources renders through render.CreateIndexes, which refuses
// Invisible, so the error surfaces here rather than a Plan going on to
// emit a visible index the database does not actually have.
func TestLiveSourcesRejectsInvisibleIndex(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:    "members",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "status", Type: schema.TextType{Width: schema.NewTextWidth(255)}}},
		Indexes: []schema.IndexDef{{Name: "members_status_idx", Columns: []string{"status"}, Invisible: true}},
	})
	require.ErrorContains(t, err, `"members_status_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsIndexKeyPrefixLength proves that an inspected table
// carrying a MySQL index key over a column prefix does not reach
// diff-live's generated desired-schema sources as a silently downgraded
// whole-column index: LiveSources renders through render.CreateIndexes,
// which refuses Keys, so the error surfaces here rather than a Plan going
// on to emit an index over more of the column than the database actually
// indexes.
func TestLiveSourcesRejectsIndexKeyPrefixLength(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:    "members",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}}},
		Indexes: []schema.IndexDef{{
			Name: "members_email_prefix_idx",
			Keys: []schema.IndexKeyDef{{Expression: "email", PrefixLength: 4}},
		}},
	})
	require.ErrorContains(t, err, `"members_email_prefix_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

func TestDiffLiveMatchesInlinePrimaryKeyUnderMySQLIdentifierRules(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (ID bigint PRIMARY KEY);")
	liveSources, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	live, err := analyzer.Parse(liveSources)
	require.NoError(t, err)

	plan, err := analyzer.Diff(baseline, live)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffLiveMatchesMySQLQuotedIdentifiersToOrdinaryIdentifiers(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id bigint); CREATE INDEX members_id_idx ON members (id);")
	liveSources, err := analyzer.LiveSources(schema.TableDef{
		Name:    "members",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Nullable: true}},
		Indexes: []schema.IndexDef{{Name: "members_id_idx", Columns: []string{"id"}}},
	})
	require.NoError(t, err)
	live, err := analyzer.Parse(liveSources)
	require.NoError(t, err)

	plan, err := analyzer.Diff(baseline, live)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)

	differentCase, err := analyzer.LiveSources(schema.TableDef{
		Name:    "Members",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Nullable: true}},
		Indexes: []schema.IndexDef{{Name: "members_id_idx", Columns: []string{"id"}}},
	})
	require.NoError(t, err)
	otherLive, err := analyzer.Parse(differentCase)
	require.NoError(t, err)
	_, err = analyzer.Diff(live, otherLive)
	require.ErrorContains(t, err, "table members was removed")
}

func TestValidateLivePlanUsesLowerCaseTableNames(t *testing.T) {
	analyzer := mysql.NewWithLowerCaseTableNames(mysql.LowerCaseTableNamesLowercase)
	err := analyzer.ValidateLivePlan(diff.Plan{
		Dialect: "mysql",
		Statements: []diff.PlannedStatement{{
			Source: "create_table.sql",
			SQL:    "CREATE TABLE Members (id bigint);",
		}},
	}, "members")
	require.NoError(t, err)
}

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
		Statements: []diff.PlannedStatement{
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
		Statements: []diff.PlannedStatement{
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

func TestDiffMatchesCaseInsensitiveColumnsAndIndexes(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE `Members` (`ID` bigint PRIMARY KEY, `Email` text);\nCREATE INDEX `Members_Email_IDX` ON `Members` (`Email`);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE `Members` (`id` bigint PRIMARY KEY, `email` text);\nCREATE INDEX `members_email_idx` ON `Members` (`email`);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

func TestDiffAllowsSameNamedIndexesOnDifferentTables(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE TABLE projects (id bigint PRIMARY KEY, name text);
		CREATE INDEX shared_idx ON members (email);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE TABLE projects (id bigint PRIMARY KEY, name text);
		CREATE INDEX shared_idx ON members (email);
		CREATE INDEX shared_idx ON projects (name);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{{
		Source:  "001_create_index_projects_shared_idx.sql",
		SQL:     "CREATE INDEX shared_idx ON projects (name);\n",
		Summary: "create index shared_idx",
	}}, plan.Statements)
}

func TestDiffGeneratedSQLRetainsTargetIdentifierSpelling(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE `Members` (`ID` bigint PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE `Members` (`ID` bigint PRIMARY KEY, `Email` text);\nCREATE INDEX `Members_Email_IDX` ON `Members` (`Email`);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, []diff.PlannedStatement{
		{
			Source:  "001_add_column_members_email.sql",
			SQL:     "ALTER TABLE `Members` ADD COLUMN `Email` text;\n",
			Summary: "add column Members.Email",
		},
		{
			Source:  "002_create_index_members_members_email_idx.sql",
			SQL:     "CREATE INDEX `Members_Email_IDX` ON `Members` (`Email`);\n",
			Summary: "create index Members_Email_IDX",
		},
	}, plan.Statements)
}

func TestDiffMatchesTablesAccordingToLowerCaseTableNames(t *testing.T) {
	baselineSource := "CREATE TABLE `Members` (`ID` bigint PRIMARY KEY);"
	targetSource := "CREATE TABLE `members` (`id` bigint PRIMARY KEY);"

	caseSensitive := mysql.New()
	baseline := parseSnapshot(t, caseSensitive, baselineSource)
	target := parseSnapshot(t, caseSensitive, targetSource)
	_, err := caseSensitive.Diff(baseline, target)
	require.ErrorContains(t, err, "table Members was removed")

	for _, tableNames := range []mysql.LowerCaseTableNames{
		mysql.LowerCaseTableNamesLowercase,
		mysql.LowerCaseTableNamesPreserve,
	} {
		analyzer := mysql.NewWithLowerCaseTableNames(tableNames)
		baseline := parseSnapshot(t, analyzer, baselineSource)
		target := parseSnapshot(t, analyzer, targetSource)
		plan, err := analyzer.Diff(baseline, target)
		require.NoError(t, err)
		require.Empty(t, plan.Statements)
	}
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
	require.Equal(t, []diff.PlannedStatement{{
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
	require.Equal(t, []diff.PlannedStatement{{
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

func TestDiffTreatsUniqueIndexAsStable(t *testing.T) {
	analyzer := mysql.New()
	baseline := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE UNIQUE INDEX members_email_uidx ON members (email);
	`)
	target := parseSnapshot(t, analyzer, `
		CREATE TABLE members (id bigint PRIMARY KEY, email text);
		CREATE UNIQUE INDEX members_email_uidx ON members (email);
	`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.True(t, plan.Empty())
}

func TestParseRejectsUnsupportedDesiredSchemaStatement(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "views.sql", SQL: "CREATE VIEW member_names AS SELECT name FROM members;"}})
	require.ErrorContains(t, err, "must be CREATE TABLE or named CREATE INDEX")
}

func TestParseRejectsIndexForMissingTable(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: `
		CREATE TABLE members (id bigint PRIMARY KEY);
		CREATE INDEX orphan_idx ON missing (id);
	`}})
	require.ErrorContains(t, err, `mysql schema source "indexes.sql"`)
	require.ErrorContains(t, err, "missing table missing")
}

func TestParseRejectsIndexOnlySourceForMissingTable(t *testing.T) {
	analyzer := mysql.New()
	_, err := analyzer.Parse([]diff.Source{{Path: "indexes.sql", SQL: "CREATE INDEX orphan_idx ON missing (id);"}})
	require.EqualError(t, err, `mysql schema source "indexes.sql" defines index orphan_idx on missing table missing`)
}

func TestParseMatchesIndexOwnerAccordingToLowerCaseTableNames(t *testing.T) {
	source := "CREATE TABLE `Members` (id bigint PRIMARY KEY);\nCREATE INDEX member_idx ON members (id);"

	caseSensitive := mysql.New()
	_, err := caseSensitive.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.ErrorContains(t, err, "missing table members")

	for _, tableNames := range []mysql.LowerCaseTableNames{
		mysql.LowerCaseTableNamesLowercase,
		mysql.LowerCaseTableNamesPreserve,
	} {
		t.Run(fmt.Sprintf("lower_case_table_names=%d", tableNames), func(t *testing.T) {
			analyzer := mysql.NewWithLowerCaseTableNames(tableNames)
			_, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
			require.NoError(t, err)
		})
	}
}

func parseSnapshot(t *testing.T, analyzer mysql.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: source}})
	require.NoError(t, err)
	return snapshot
}
