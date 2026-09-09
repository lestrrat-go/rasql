//go:build unix

package postgresql_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLSchemaEvolutionPublicArtifactRoundTrip(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	members := dbtest.UniqueName(t, "rasql_public_members")
	projects := dbtest.UniqueName(t, "rasql_public_projects")
	tasks := dbtest.UniqueName(t, "rasql_public_tasks")
	q := quotePostgreSQLIdentifier
	baselineSQL := fmt.Sprintf(`CREATE TABLE %s ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));
CREATE TABLE %s ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "name" TEXT NOT NULL, PRIMARY KEY ("id"));
CREATE TABLE %s ("id" BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY, "project_id" BIGINT NOT NULL, "assignee_id" BIGINT NOT NULL, "title" TEXT NOT NULL, "is_open" BOOLEAN NOT NULL DEFAULT true, "created_at" TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES %s ("id") ON DELETE NO ACTION ON UPDATE NO ACTION, CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES %s ("id") ON DELETE CASCADE ON UPDATE NO ACTION);`, q(members), q(projects), q(tasks), q(members), q(projects))
	for _, statement := range strings.Split(baselineSQL, "\n") {
		if strings.TrimSpace(statement) != "" {
			_, err := database.ExecContext(ctx, statement)
			require.NoError(t, err)
		}
	}
	var memberID, projectID, taskID int64
	require.NoError(t, database.QueryRowContext(ctx, fmt.Sprintf(`INSERT INTO %s ("name") VALUES ('member') RETURNING "id"`, q(members))).Scan(&memberID))
	require.NoError(t, database.QueryRowContext(ctx, fmt.Sprintf(`INSERT INTO %s ("name") VALUES ('project') RETURNING "id"`, q(projects))).Scan(&projectID))
	require.NoError(t, database.QueryRowContext(ctx, fmt.Sprintf(`INSERT INTO %s ("project_id", "assignee_id", "title") VALUES ($1, $2, 'task') RETURNING "id"`, q(tasks)), projectID, memberID).Scan(&taskID))
	targetSQL := strings.Replace(baselineSQL, `"assignee_id" BIGINT NOT NULL`, `"assignee_id" BIGINT`, 1)
	targetSQL = strings.Replace(targetSQL, "ON DELETE NO ACTION ON UPDATE NO ACTION", "ON DELETE SET NULL ON UPDATE NO ACTION", 1)
	analyzer := postgresql.New()
	base, err := analyzer.Parse([]diff.Source{{Path: "baseline.sql", SQL: sqltext.Text(baselineSQL)}})
	require.NoError(t, err)
	target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: sqltext.Text(targetSQL)}})
	require.NoError(t, err)
	plan, err := analyzer.Diff(base, target)
	require.NoError(t, err)
	require.Empty(t, plan.Decisions)
	require.True(t, plan.Executable())
	require.Equal(t, []string{"alter_nullability_postgresql_" + tasks + "_assignee_id", "replace_constraint_postgresql_" + tasks + "_tasks_assignee_id_fkey"}, []string{plan.Operations[0].ID, plan.Operations[1].ID})
	original := snapshotPublicPlan(t, plan)
	disposable, err := plan.Resolve()
	require.NoError(t, err)
	for index := range disposable.Operations {
		disposable.Operations[index].ID = "mutated"
		disposable.Operations[index].Table = "mutated"
		disposable.Operations[index].Column = "mutated"
		disposable.Operations[index].Constraint = "mutated"
		disposable.Operations[index].Summary = "mutated"
		disposable.Operations[index].Kind = diff.OperationCreateTable
		for statementIndex := range disposable.Operations[index].Forward {
			disposable.Operations[index].Forward[statementIndex] = diff.PlannedStatement{Source: "mutated", SQL: "mutated", ReverseSQL: "mutated", Summary: "mutated"}
		}
		for statementIndex := range disposable.Operations[index].Reverse {
			disposable.Operations[index].Reverse[statementIndex] = diff.PlannedStatement{Source: "mutated", SQL: "mutated", ReverseSQL: "mutated", Summary: "mutated"}
		}
	}
	for index := range disposable.Statements {
		disposable.Statements[index] = diff.PlannedStatement{Source: "mutated", SQL: "mutated", ReverseSQL: "mutated", Summary: "mutated"}
	}
	disposable.Decisions = []diff.RequiredDecision{{ID: "mutated"}}
	disposable.IrreversibleReason = "mutated"
	require.Equal(t, original, snapshotPublicPlan(t, plan))
	root := t.TempDir()
	resolved, err := plan.Resolve()
	require.NoError(t, err)
	baselineRow := readTaskRow(t, database, tasks, taskID)
	baselineMembers := inspectPostgreSQLTable(t, database, members)
	baselineProjects := inspectPostgreSQLTable(t, database, projects)
	baselineTasks := inspectPostgreSQLTable(t, database, tasks)
	quotedTasks, quotedMembers := q(tasks), q(members)
	expectedForward := []diff.PlannedStatement{
		{Source: "001_drop_constraint_" + tasks + "_tasks_assignee_id_fkey.sql", SQL: fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT \"tasks_assignee_id_fkey\";\n", quotedTasks), ReverseSQL: fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT \"tasks_assignee_id_fkey\" FOREIGN KEY (\"assignee_id\") REFERENCES %s (\"id\") ON DELETE NO ACTION ON UPDATE NO ACTION;\n", quotedTasks, quotedMembers), Summary: "replace constraint " + tasks + ".tasks_assignee_id_fkey"},
		{Source: "002_alter_nullability_" + tasks + "_assignee_id.sql", SQL: fmt.Sprintf("ALTER TABLE %s ALTER COLUMN \"assignee_id\" DROP NOT NULL;\n", quotedTasks), ReverseSQL: fmt.Sprintf("ALTER TABLE %s ALTER COLUMN \"assignee_id\" SET NOT NULL;\n", quotedTasks), Summary: "alter nullability " + tasks + ".assignee_id"},
		{Source: "003_add_constraint_" + tasks + "_tasks_assignee_id_fkey.sql", SQL: fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT \"tasks_assignee_id_fkey\" FOREIGN KEY (\"assignee_id\") REFERENCES %s (\"id\") ON DELETE SET NULL ON UPDATE NO ACTION;\n", quotedTasks, quotedMembers), ReverseSQL: fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT \"tasks_assignee_id_fkey\";\n", quotedTasks), Summary: "replace constraint " + tasks + ".tasks_assignee_id_fkey"},
	}
	require.Equal(t, expectedForward, resolved.Statements)
	expectedOperations := []diff.ProposedOperation{
		{ID: "alter_nullability_postgresql_" + tasks + "_assignee_id", Table: tasks, Column: "assignee_id", Summary: "alter nullability " + tasks + ".assignee_id", Kind: diff.OperationAlterNullability, Forward: []diff.PlannedStatement{expectedForward[1]}, Reverse: []diff.PlannedStatement{{Source: expectedForward[1].Source, SQL: expectedForward[1].ReverseSQL, ReverseSQL: expectedForward[1].SQL, Summary: expectedForward[1].Summary}}},
		{ID: "replace_constraint_postgresql_" + tasks + "_tasks_assignee_id_fkey", Table: tasks, Constraint: "tasks_assignee_id_fkey", Summary: "replace constraint " + tasks + ".tasks_assignee_id_fkey", Kind: diff.OperationReplaceConstraint, Forward: []diff.PlannedStatement{expectedForward[0], expectedForward[2]}, Reverse: []diff.PlannedStatement{{Source: expectedForward[2].Source, SQL: expectedForward[2].ReverseSQL, ReverseSQL: expectedForward[2].SQL, Summary: expectedForward[2].Summary}, {Source: expectedForward[0].Source, SQL: expectedForward[0].ReverseSQL, ReverseSQL: expectedForward[0].SQL, Summary: expectedForward[0].Summary}}},
	}
	require.Equal(t, expectedOperations, resolved.Operations)
	migration := writeLoadPostgreSQLPlan(t, root, "001_public_taskboard", resolved)
	artifactDir := filepath.Join(root, migration.ID)
	require.Equal(t, []string{"001_drop_constraint_" + tasks + "_tasks_assignee_id_fkey.down.sql", "001_drop_constraint_" + tasks + "_tasks_assignee_id_fkey.up.sql", "002_alter_nullability_" + tasks + "_assignee_id.down.sql", "002_alter_nullability_" + tasks + "_assignee_id.up.sql", "003_add_constraint_" + tasks + "_tasks_assignee_id_fkey.down.sql", "003_add_constraint_" + tasks + "_tasks_assignee_id_fkey.up.sql"}, artifactNames(t, artifactDir))
	for _, statement := range expectedForward {
		stem := strings.TrimSuffix(statement.Source, ".sql")
		require.Equal(t, string(statement.SQL), readArtifact(t, artifactDir, stem+".up.sql"))
		require.Equal(t, string(statement.ReverseSQL), readArtifact(t, artifactDir, stem+".down.sql"))
	}
	assertLoadedStatements(t, migration.Statements, expectedForward, ".up.sql")
	assertLoadedStatements(t, migration.Down, []diff.PlannedStatement{expectedForward[2], expectedForward[1], expectedForward[0]}, ".down.sql")
	runner, err := migrate.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	applied, err := runner.Apply(ctx, migrate.AllPending(), migration)
	require.NoError(t, err)
	require.Equal(t, []string{migration.ID}, migrationIDs(applied))
	inspected := inspectPostgreSQLTable(t, database, tasks)
	require.Equal(t, schema.IdentityAlways, requirePostgreSQLColumn(t, inspected, "id").Identity)
	require.False(t, requirePostgreSQLColumn(t, inspected, "id").Nullable)
	require.True(t, requirePostgreSQLColumn(t, inspected, "assignee_id").Nullable)
	assignee := requirePostgreSQLForeignKey(t, inspected, "tasks_assignee_id_fkey")
	require.Equal(t, []string{"assignee_id"}, assignee.Columns)
	require.Equal(t, members, assignee.ReferencedTable)
	require.Equal(t, []string{"id"}, assignee.ReferencedColumns)
	require.Equal(t, schema.SetNull, assignee.OnDelete)
	require.Equal(t, schema.NoAction, assignee.OnUpdate)
	projectFK := requirePostgreSQLForeignKey(t, inspected, "tasks_project_id_fkey")
	require.Equal(t, []string{"project_id"}, projectFK.Columns)
	require.Equal(t, projects, projectFK.ReferencedTable)
	require.Equal(t, []string{"id"}, projectFK.ReferencedColumns)
	require.Equal(t, schema.Cascade, projectFK.OnDelete)
	require.Equal(t, schema.NoAction, projectFK.OnUpdate)
	for _, name := range []string{"is_open", "created_at"} {
		require.Equal(t, requirePostgreSQLColumn(t, baselineTasks, name).Default, requirePostgreSQLColumn(t, inspected, name).Default)
	}
	row := readTaskRow(t, database, tasks, taskID)
	require.Equal(t, taskRow{ID: taskID, ProjectID: projectID, AssigneeID: memberID, Title: "task", IsOpen: true, CreatedAt: baselineRow.CreatedAt}, row)
	assertPublicRoundtrip(t, analyzer, target, database, members, projects, tasks)
	_, err = runner.Revert(ctx, migrate.Steps(1), migration)
	require.NoError(t, err)
	reverted := inspectPostgreSQLTable(t, database, tasks)
	require.False(t, requirePostgreSQLColumn(t, reverted, "assignee_id").Nullable)
	revertedAssignee := requirePostgreSQLForeignKey(t, reverted, "tasks_assignee_id_fkey")
	require.Equal(t, schema.NoAction, revertedAssignee.OnDelete)
	require.Equal(t, schema.NoAction, revertedAssignee.OnUpdate)
	require.Equal(t, baselineRow, readTaskRow(t, database, tasks, taskID))
	require.Equal(t, baselineMembers, inspectPostgreSQLTable(t, database, members))
	require.Equal(t, baselineProjects, inspectPostgreSQLTable(t, database, projects))
	require.Equal(t, baselineTasks, reverted)
	assertPublicRoundtrip(t, analyzer, base, database, members, projects, tasks)
	status, err := runner.Status(ctx, migration)
	require.NoError(t, err)
	require.Equal(t, []migrate.StatusEntry{{ID: migration.ID, State: migrate.StatusPending, Reversible: true}}, status)
}

func TestPostgreSQLSchemaEvolutionOpaqueBackfillArtifact(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	tasks := dbtest.UniqueName(t, "rasql_public_backfill_tasks")
	q := quotePostgreSQLIdentifier
	_, err := database.ExecContext(t.Context(), fmt.Sprintf(`CREATE TABLE %s ("id" BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, "owner_id" BIGINT NOT NULL); INSERT INTO %s ("owner_id") VALUES (7);`, q(tasks), q(tasks)))
	require.NoError(t, err)
	analyzer := postgresql.New()
	base, err := analyzer.Parse([]diff.Source{{Path: "baseline.sql", SQL: sqltext.Text(fmt.Sprintf("CREATE TABLE %s (\"id\" BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, \"owner_id\" BIGINT NOT NULL);", q(tasks)))}})
	require.NoError(t, err)
	target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: sqltext.Text(fmt.Sprintf("CREATE TABLE %s (\"id\" BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, \"owner_id\" BIGINT NOT NULL, \"owner_label\" TEXT NOT NULL);", q(tasks)))}})
	require.NoError(t, err)
	plan, err := analyzer.Diff(base, target)
	require.NoError(t, err)
	require.False(t, plan.Executable())
	require.Nil(t, plan.Statements)
	require.Len(t, plan.Operations, 1)
	require.Equal(t, diff.ProposedOperation{ID: "add_column_postgresql_" + tasks + "_owner_label", Table: tasks, Column: "owner_label", Summary: "add column " + tasks + ".owner_label", Kind: diff.OperationAddColumn}, plan.Operations[0])
	decision := plan.Decisions[0]
	require.Equal(t, "backfill_postgresql_"+tasks+"_owner_label", decision.ID)
	require.Equal(t, diff.RequiredDecision{ID: decision.ID, Kind: diff.DecisionBackfill, Table: tasks, Column: "owner_label", Target: "owner_label", Reason: "required column needs an application-specific backfill"}, decision)
	original := snapshotPublicPlan(t, plan)
	root := t.TempDir()
	err = diff.WriteMigration(filepath.Join(root, "001_opaque"), plan)
	require.ErrorContains(t, err, "migrate diff: unresolved decisions:")
	require.ErrorContains(t, err, decision.ID)
	entries, readErr := os.ReadDir(root)
	require.NoError(t, readErr)
	require.Empty(t, entries)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: decision.ID, BackfillSQL: fmt.Sprintf(`UPDATE %s SET "owner_label" = 'owner-' || "owner_id";`, q(tasks))})
	require.NoError(t, err)
	disposable, err := plan.Resolve(diff.Resolution{DecisionID: decision.ID, BackfillSQL: fmt.Sprintf(`UPDATE %s SET "owner_label" = 'disposable-' || "owner_id";`, q(tasks))})
	require.NoError(t, err)
	for index := range disposable.Operations {
		disposable.Operations[index].ID = "mutated"
		disposable.Operations[index].Table = "mutated"
		disposable.Operations[index].Column = "mutated"
		disposable.Operations[index].Constraint = "mutated"
		disposable.Operations[index].Summary = "mutated"
		disposable.Operations[index].Kind = diff.OperationCreateTable
		for statementIndex := range disposable.Operations[index].Forward {
			disposable.Operations[index].Forward[statementIndex] = diff.PlannedStatement{Source: "mutated", SQL: "mutated", ReverseSQL: "mutated", Summary: "mutated"}
		}
		for statementIndex := range disposable.Operations[index].Reverse {
			disposable.Operations[index].Reverse[statementIndex] = diff.PlannedStatement{Source: "mutated", SQL: "mutated", ReverseSQL: "mutated", Summary: "mutated"}
		}
	}
	for index := range disposable.Statements {
		disposable.Statements[index] = diff.PlannedStatement{Source: "mutated", SQL: "mutated", ReverseSQL: "mutated", Summary: "mutated"}
	}
	disposable.Decisions = []diff.RequiredDecision{{ID: "mutated"}}
	disposable.IrreversibleReason = "mutated"
	require.Equal(t, original, snapshotPublicPlan(t, plan))
	expectedSQL := fmt.Sprintf("ALTER TABLE %s ADD COLUMN \"owner_label\" text;\nUPDATE %s SET \"owner_label\" = 'owner-' || \"owner_id\";\nALTER TABLE %s ALTER COLUMN \"owner_label\" SET NOT NULL;\n", q(tasks), q(tasks), q(tasks))
	require.Equal(t, diff.ProposedOperation{ID: "add_column_postgresql_" + tasks + "_owner_label", Table: tasks, Column: "owner_label", Summary: "add column " + tasks + ".owner_label", Kind: diff.OperationAddColumn, Forward: []diff.PlannedStatement{{Source: "001_add_column_" + tasks + "_owner_label.sql", SQL: expectedSQL, ReverseSQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN \"owner_label\";\n", q(tasks)), Summary: "add column " + tasks + ".owner_label"}}, Reverse: []diff.PlannedStatement{{Source: "001_add_column_" + tasks + "_owner_label.sql", SQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN \"owner_label\";\n", q(tasks)), ReverseSQL: expectedSQL, Summary: "add column " + tasks + ".owner_label"}}}, resolved.Operations[0])
	require.Equal(t, resolved.Operations[0].Forward, resolved.Statements)
	require.Equal(t, "caller-supplied backfill has no inferred reverse", resolved.IrreversibleReason)
	root = t.TempDir()
	migration := writeLoadPostgreSQLPlan(t, root, "001_opaque", resolved)
	files, err := os.ReadDir(filepath.Join(root, migration.ID))
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, expectedSQL, readArtifact(t, filepath.Join(root, migration.ID), "001_add_column_"+tasks+"_owner_label.up.sql"))
	require.Equal(t, "caller-supplied backfill has no inferred reverse\n", readArtifact(t, filepath.Join(root, migration.ID), ".rasql-irreversible"))
	require.Empty(t, migration.Down)
	runner, err := migrate.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	applied, err := runner.Apply(t.Context(), migrate.AllPending(), migration)
	require.NoError(t, err)
	require.Equal(t, []string{migration.ID}, migrationIDs(applied))
	inspected := inspectPostgreSQLTable(t, database, tasks)
	require.False(t, requirePostgreSQLColumn(t, inspected, "owner_label").Nullable)
	require.Equal(t, schema.IdentityAlways, requirePostgreSQLColumn(t, inspected, "id").Identity)
	var label string
	require.NoError(t, database.QueryRowContext(t.Context(), fmt.Sprintf(`SELECT "owner_label" FROM %s WHERE "owner_id" = 7`, q(tasks))).Scan(&label))
	require.Equal(t, "owner-7", label)
	sources, err := analyzer.LiveSources(inspected)
	require.NoError(t, err)
	roundtrip, err := analyzer.Parse(sources)
	require.NoError(t, err)
	roundtripPlan, err := analyzer.Diff(roundtrip, target)
	require.NoError(t, err)
	require.Empty(t, roundtripPlan.Operations)
	require.Empty(t, roundtripPlan.Decisions)
	require.Empty(t, roundtripPlan.Statements)
	require.Empty(t, roundtripPlan.IrreversibleReason)
	beforeRevert := inspectPostgreSQLTable(t, database, tasks)
	beforeRevertRow := readBackfillRow(t, database, tasks)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), migration)
	require.Error(t, err)
	require.ErrorContains(t, err, migration.ID)
	require.EqualError(t, err, `migrate: migration "001_opaque" has no reverse SQL source: caller-supplied backfill has no inferred reverse`)
	require.Equal(t, beforeRevert, inspectPostgreSQLTable(t, database, tasks))
	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, []migrate.StatusEntry{{ID: migration.ID, State: migrate.StatusApplied, IrreversibleReason: "caller-supplied backfill has no inferred reverse"}}, status)
	require.Equal(t, beforeRevertRow, readBackfillRow(t, database, tasks))
}

type backfillRow struct {
	ID, OwnerID int64
	Label       string
}

func readBackfillRow(t *testing.T, database *sql.DB, table string) backfillRow {
	t.Helper()
	var row backfillRow
	require.NoError(t, database.QueryRowContext(t.Context(), fmt.Sprintf(`SELECT "id", "owner_id", "owner_label" FROM %s WHERE "owner_id" = 7`, quotePostgreSQLIdentifier(table))).Scan(&row.ID, &row.OwnerID, &row.Label))
	return row
}

func migrationIDs(migrations []migrate.Migration) []string {
	ids := make([]string, len(migrations))
	for index := range migrations {
		ids[index] = migrations[index].ID
	}
	return ids
}

func artifactNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for index := range entries {
		names[index] = entries[index].Name()
	}
	return names
}

func assertLoadedStatements(t *testing.T, actual []migrate.Statement, expected []diff.PlannedStatement, suffix string) {
	t.Helper()
	require.Len(t, actual, len(expected))
	for index := range expected {
		require.Equal(t, strings.TrimSuffix(expected[index].Source, ".sql")+suffix, actual[index].Source)
		sql := expected[index].SQL
		if suffix == ".down.sql" {
			sql = expected[index].ReverseSQL
		}
		require.Equal(t, string(sql), string(actual[index].SQL))
	}
}

type taskRow struct {
	ID, ProjectID, AssigneeID int64
	Title                     string
	IsOpen                    bool
	CreatedAt                 time.Time
}

func readTaskRow(t *testing.T, database *sql.DB, table string, id int64) taskRow {
	t.Helper()
	var row taskRow
	require.NoError(t, database.QueryRowContext(t.Context(), fmt.Sprintf(`SELECT "id", "project_id", "assignee_id", "title", "is_open", "created_at" FROM %s WHERE "id" = $1`, quotePostgreSQLIdentifier(table)), id).Scan(&row.ID, &row.ProjectID, &row.AssigneeID, &row.Title, &row.IsOpen, &row.CreatedAt))
	return row
}

func assertPublicRoundtrip(t *testing.T, analyzer postgresql.Analyzer, expected diff.Snapshot, database *sql.DB, names ...string) {
	t.Helper()
	sources := make([]diff.Source, 0, len(names))
	for _, name := range names {
		table := inspectPostgreSQLTable(t, database, name)
		liveSources, err := analyzer.LiveSources(table)
		require.NoError(t, err)
		sources = append(sources, liveSources...)
	}
	live, err := analyzer.Parse(sources)
	require.NoError(t, err)
	plan, err := analyzer.Diff(live, expected)
	require.NoError(t, err)
	require.Empty(t, plan.Operations)
	require.Empty(t, plan.Decisions)
	require.Empty(t, plan.Statements)
	require.Empty(t, plan.IrreversibleReason)
}

func writeLoadPostgreSQLPlan(t *testing.T, root, id string, plan diff.Plan) migrate.Migration {
	t.Helper()
	require.NoError(t, diff.WriteMigration(filepath.Join(root, id), plan))
	loaded, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, id, loaded[0].ID)
	return loaded[0]
}

func inspectPostgreSQLTable(t *testing.T, db *sql.DB, name string) schema.TableDef {
	t.Helper()
	inspector, err := inspect.New(db, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), name)
	require.NoError(t, err)
	return table
}

func requirePostgreSQLColumn(t *testing.T, table schema.TableDef, name string) schema.ColumnDef {
	t.Helper()
	for _, column := range table.Columns {
		if column.Name == name {
			return column
		}
	}
	t.Fatalf("column %q is missing", name)
	return schema.ColumnDef{}
}

func requirePostgreSQLForeignKey(t *testing.T, table schema.TableDef, name string) schema.ForeignKeyDef {
	t.Helper()
	for _, foreignKey := range table.ForeignKeys {
		if foreignKey.Name == name {
			return foreignKey
		}
	}
	t.Fatalf("foreign key %q is missing", name)
	return schema.ForeignKeyDef{}
}

func readArtifact(t *testing.T, directory, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, name))
	require.NoError(t, err)
	return string(contents)
}

func snapshotPublicPlan(t *testing.T, plan diff.Plan) []byte {
	t.Helper()
	contents, err := json.Marshal(struct {
		Operations         []diff.ProposedOperation
		Decisions          []diff.RequiredDecision
		Statements         []diff.PlannedStatement
		IrreversibleReason string
	}{plan.Operations, plan.Decisions, plan.Statements, plan.IrreversibleReason})
	require.NoError(t, err)
	return contents
}

func quotePostgreSQLIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
