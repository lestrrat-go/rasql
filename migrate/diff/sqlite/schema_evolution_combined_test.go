package sqlite_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	sqliteanalyzer "github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestSchemaEvolutionSQLiteCompletePublicRoundTrip proves one populated
// database through planning, durable migration artifacts, and public runner APIs.
func TestSchemaEvolutionSQLiteCompletePublicRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", "file:complete-public-roundtrip?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), `PRAGMA foreign_keys = ON;
CREATE TABLE parents (
  id INTEGER PRIMARY KEY,
  label TEXT COLLATE NOCASE NOT NULL UNIQUE CHECK (length(label) > 0)
);
CREATE TABLE tasks (
  id INTEGER PRIMARY KEY,
  owner_id INTEGER NOT NULL,
  title TEXT COLLATE NOCASE NOT NULL,
  status TEXT COLLATE NOCASE NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL
);
CREATE INDEX tasks_title_idx ON tasks(title);
INSERT INTO parents VALUES (1, 'ALPHA'), (2, 'Beta');
INSERT INTO tasks VALUES (10, 1, 'one', 'open'), (20, 2, 'two', 'closed');`)
	require.NoError(t, err)

	analyzer := sqliteanalyzer.New()
	baseline := completeSnapshot(t, analyzer, `CREATE TABLE parents (
  id INTEGER PRIMARY KEY,
  label TEXT COLLATE NOCASE NOT NULL UNIQUE CHECK (length(label) > 0)
);
CREATE TABLE tasks (
  id INTEGER PRIMARY KEY,
  owner_id INTEGER NOT NULL,
  title TEXT COLLATE NOCASE NOT NULL,
  status TEXT COLLATE NOCASE NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL
);
CREATE INDEX tasks_title_idx ON tasks(title);`)
	target := completeSnapshot(t, analyzer, `CREATE TABLE parents (
  id INTEGER PRIMARY KEY,
  label TEXT COLLATE NOCASE NOT NULL UNIQUE CHECK (length(label) > 0)
);
CREATE TABLE tasks (
  id INTEGER PRIMARY KEY,
  owner_id INTEGER,
  task_title TEXT COLLATE NOCASE NOT NULL,
  status TEXT COLLATE NOCASE NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  due_on DATE COLLATE NOCASE,
  owner_label VARCHAR(64) COLLATE NOCASE NOT NULL,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL,
  CONSTRAINT tasks_title_unique UNIQUE (task_title)
);
CREATE INDEX tasks_owner_idx ON tasks(owner_id);
CREATE INDEX tasks_task_title_idx ON tasks(task_title);`)
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqliteanalyzer.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, "sqlite", plan.Dialect)
	require.Len(t, plan.Operations, 1)
	require.Equal(t, diff.OperationRebuildTable, plan.Operations[0].Kind)
	require.Equal(t, "tasks", plan.Operations[0].Table)
	require.Equal(t, "rebuild table tasks", plan.Operations[0].Summary)
	require.Equal(t, diff.ProposedOperation{ID: "rebuild_table_sqlite_tasks", Table: "tasks", Summary: "rebuild table tasks", Kind: diff.OperationRebuildTable}, plan.Operations[0])
	require.NotEmpty(t, plan.Decisions)
	require.Equal(t, []diff.RequiredDecision{
		{ID: "rename_sqlite_tasks_task_title", Table: "tasks", Column: "task_title", Baseline: "title", Target: "task_title", Reason: "column rename requires caller confirmation", Kind: diff.DecisionRename},
		{ID: "backfill_sqlite_tasks_owner_label", Table: "tasks", Column: "owner_label", Target: "owner_label", Reason: "required column needs an application-specific backfill", Kind: diff.DecisionBackfill},
	}, plan.Decisions)

	decisionIDs := make([]string, 0, len(plan.Decisions))
	resolutions := make([]diff.Resolution, 0, len(plan.Decisions))
	for _, decision := range plan.Decisions {
		decisionIDs = append(decisionIDs, decision.ID)
		resolution := diff.Resolution{DecisionID: decision.ID}
		if decision.Kind == diff.DecisionRename {
			require.Equal(t, "title", decision.Baseline)
			require.Equal(t, "task_title", decision.Target)
			resolution.RenameFrom = decision.Baseline
		} else {
			require.Equal(t, diff.DecisionBackfill, decision.Kind)
			require.Equal(t, "owner_label", decision.Column)
			resolution.BackfillSQL = "UPDATE tasks SET owner_label = 'owner-' || owner_id;"
		}
		resolutions = append(resolutions, resolution)
	}
	sort.Strings(decisionIDs)
	require.Equal(t, []string{"backfill_sqlite_tasks_owner_label", "rename_sqlite_tasks_task_title"}, decisionIDs)
	resolved, err := plan.Resolve(resolutions...)
	require.NoError(t, err)
	expectedStatements := []diff.PlannedStatement{
		{Source: "001_0_stage_owner_label.sql", SQL: "ALTER TABLE tasks ADD COLUMN owner_label varchar(64) COLLATE nocase;\n", ReverseSQL: "-- staging column is removed by the structural rebuild\nCREATE INDEX tasks_title_idx ON tasks (title);\n", Summary: "stage tasks.owner_label"},
		{Source: "002_1_backfill_owner_label.sql", SQL: "UPDATE tasks SET owner_label = 'owner-' || owner_id;\n", ReverseSQL: "-- backfill is removed by the structural rebuild\n", Summary: "backfill tasks.owner_label"},
		{Source: "003_2_create_rebuild.sql", SQL: "CREATE TABLE tasks__rasql_rebuild (id integer PRIMARY KEY, owner_id integer, task_title text COLLATE nocase NOT NULL, status text COLLATE nocase NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')), due_on date COLLATE nocase, owner_label varchar(64) COLLATE nocase NOT NULL, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents (id) ON DELETE CASCADE ON UPDATE SET NULL, CONSTRAINT tasks_title_unique UNIQUE (task_title));\n\n", ReverseSQL: "ALTER TABLE tasks__rasql_rebuild_2 RENAME TO tasks;\n", Summary: "create rebuild table tasks"},
		{Source: "004_3_copy_rebuild.sql", SQL: "INSERT INTO tasks__rasql_rebuild (due_on, id, owner_id, owner_label, status, task_title) SELECT NULL, id, owner_id, owner_label, status, title FROM tasks;\n", ReverseSQL: "DROP TABLE tasks;\n", Summary: "copy tasks"},
		{Source: "005_4_drop_table.sql", SQL: "DROP TABLE tasks;\n", ReverseSQL: "INSERT INTO tasks__rasql_rebuild_2 (id, owner_id, status, title) SELECT id, owner_id, status, task_title FROM tasks;\n", Summary: "drop tasks"},
		{Source: "006_5_rename_rebuild.sql", SQL: "ALTER TABLE tasks__rasql_rebuild RENAME TO tasks;\n", ReverseSQL: "CREATE TABLE tasks__rasql_rebuild_2 (id integer PRIMARY KEY, owner_id integer NOT NULL, title text COLLATE nocase NOT NULL, status text COLLATE nocase NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')), CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents (id) ON DELETE CASCADE ON UPDATE SET NULL);\n\n", Summary: "rename rebuild tasks"},
		{Source: "007_6_index_tasks_owner_idx.sql", SQL: "CREATE INDEX tasks_owner_idx ON tasks (owner_id);\n", ReverseSQL: "DROP INDEX tasks_owner_idx;\n", Summary: "create index tasks_owner_idx"},
		{Source: "008_6_index_tasks_task_title_idx.sql", SQL: "CREATE INDEX tasks_task_title_idx ON tasks (task_title);\n", ReverseSQL: "DROP INDEX tasks_task_title_idx;\n", Summary: "create index tasks_task_title_idx"},
	}
	require.Equal(t, expectedStatements, resolved.Statements)
	require.Empty(t, resolved.Decisions)
	require.NotEmpty(t, resolved.Statements)
	require.Len(t, resolved.Operations, 1)
	require.Equal(t, resolved.Statements, resolved.Operations[0].Forward)
	require.Len(t, resolved.Operations[0].Reverse, len(resolved.Statements))
	for i, forward := range resolved.Statements {
		reverse := resolved.Operations[0].Reverse[len(resolved.Statements)-1-i]
		require.Equal(t, forward.Source, reverse.Source)
		require.Equal(t, forward.ReverseSQL, reverse.SQL)
		require.Equal(t, forward.SQL, reverse.ReverseSQL)
		require.NotEmpty(t, forward.SQL)
		require.NotEmpty(t, forward.ReverseSQL)
	}

	output := t.TempDir()
	migrationDir := filepath.Join(output, "001_complete")
	require.NoError(t, diff.WriteMigration(migrationDir, resolved))
	loaded, err := migrationdir.Load(output)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "001_complete", loaded[0].ID)
	require.Len(t, loaded[0].Statements, len(resolved.Statements))
	require.Len(t, loaded[0].Down, len(resolved.Statements))
	entries, err := os.ReadDir(migrationDir)
	require.NoError(t, err)
	actualNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		actualNames = append(actualNames, entry.Name())
	}
	expectedNames := make([]string, 0, len(resolved.Statements)*2)
	for _, statement := range resolved.Statements {
		expectedNames = append(expectedNames, strings.TrimSuffix(statement.Source, ".sql")+".up.sql")
		expectedNames = append(expectedNames, strings.TrimSuffix(statement.Source, ".sql")+".down.sql")
	}
	sort.Strings(expectedNames)
	require.Equal(t, expectedNames, actualNames)
	for i, statement := range resolved.Statements {
		upName := strings.TrimSuffix(statement.Source, ".sql") + ".up.sql"
		downName := strings.TrimSuffix(statement.Source, ".sql") + ".down.sql"
		require.Equal(t, upName, loaded[0].Statements[i].Source)
		require.Equal(t, statement.SQL, string(loaded[0].Statements[i].SQL))
		require.Equal(t, downName, loaded[0].Down[len(resolved.Statements)-1-i].Source)
		require.Equal(t, statement.ReverseSQL, string(loaded[0].Down[len(resolved.Statements)-1-i].SQL))
		upBytes, readErr := os.ReadFile(filepath.Join(migrationDir, upName))
		require.NoError(t, readErr)
		require.Equal(t, statement.SQL, string(upBytes))
		downBytes, readErr := os.ReadFile(filepath.Join(migrationDir, downName))
		require.NoError(t, readErr)
		require.Equal(t, statement.ReverseSQL, string(downBytes))
	}

	runner, err := migrate.NewWithHistoryTable(db, dialect.SQLite(), "complete_history")
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), loaded[0])
	require.NoError(t, err)
	assertCompleteTarget(t, db)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), loaded[0])
	require.NoError(t, err)
	assertCompleteBaseline(t, db)
}

func completeSnapshot(t *testing.T, analyzer sqliteanalyzer.Analyzer, source string) diff.Snapshot {
	t.Helper()
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "complete.sql", SQL: sqltext.Text(source)}})
	require.NoError(t, err)
	return snapshot
}

func assertCompleteTarget(t *testing.T, db *sql.DB) {
	t.Helper()
	var foreignKeys int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
	rows, err := db.QueryContext(t.Context(), "SELECT id, owner_id, task_title, status, due_on, owner_label FROM tasks ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for _, expected := range []struct {
		id, owner            int
		title, status, label string
	}{{10, 1, "one", "open", "owner-1"}, {20, 2, "two", "closed", "owner-2"}} {
		require.True(t, rows.Next())
		var id, owner int
		var title, status, label string
		var due sql.NullString
		require.NoError(t, rows.Scan(&id, &owner, &title, &status, &due, &label))
		require.Equal(t, expected.id, id)
		require.Equal(t, expected.owner, owner)
		require.Equal(t, expected.title, title)
		require.Equal(t, expected.status, status)
		require.False(t, due.Valid)
		require.Equal(t, expected.label, label)
	}
	require.False(t, rows.Next())

	var tableSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'tasks'").Scan(&tableSQL))
	require.Equal(t, "CREATE TABLE \"tasks\" (id integer PRIMARY KEY, owner_id integer, task_title text COLLATE nocase NOT NULL, status text COLLATE nocase NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')), due_on date COLLATE nocase, owner_label varchar(64) COLLATE nocase NOT NULL, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents (id) ON DELETE CASCADE ON UPDATE SET NULL, CONSTRAINT tasks_title_unique UNIQUE (task_title))", tableSQL)
	assertTargetColumns(t, db)
	assertTargetForeignKey(t, db)
	assertIndexSQL(t, db, "tasks_owner_idx", "CREATE INDEX tasks_owner_idx ON tasks (owner_id)")
	assertIndexSQL(t, db, "tasks_task_title_idx", "CREATE INDEX tasks_task_title_idx ON tasks (task_title)")
	assertUniqueIndex(t, db, "tasks")
	assertMissingObject(t, db, "tasks_title_idx")
	assertMissingRebuildTables(t, db)
	assertHistory(t, db, "complete_history", "001_complete", 1)
}

func assertCompleteBaseline(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT id, owner_id, title, status FROM tasks ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for _, expected := range []struct {
		id, owner     int
		title, status string
	}{{10, 1, "one", "open"}, {20, 2, "two", "closed"}} {
		require.True(t, rows.Next())
		var id, owner int
		var title, status string
		require.NoError(t, rows.Scan(&id, &owner, &title, &status))
		require.Equal(t, expected.id, id)
		require.Equal(t, expected.owner, owner)
		require.Equal(t, expected.title, title)
		require.Equal(t, expected.status, status)
	}
	require.False(t, rows.Next())
	var tableSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'tasks'").Scan(&tableSQL))
	require.Equal(t, "CREATE TABLE \"tasks\" (id integer PRIMARY KEY, owner_id integer NOT NULL, title text COLLATE nocase NOT NULL, status text COLLATE nocase NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')), CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents (id) ON DELETE CASCADE ON UPDATE SET NULL)", tableSQL)
	assertIndexSQL(t, db, "tasks_title_idx", "CREATE INDEX tasks_title_idx ON tasks (title)")
	assertMissingObject(t, db, "tasks_owner_idx")
	assertMissingObject(t, db, "tasks_task_title_idx")
	assertMissingRebuildTables(t, db)
	assertHistory(t, db, "complete_history", "001_complete", 0)
}

func assertTargetColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "PRAGMA table_info('tasks')")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	type column struct {
		name, typ, defaultValue string
		notNull                 int
	}
	var got []column
	for rows.Next() {
		var cid, pk int
		var value sql.NullString
		var item column
		require.NoError(t, rows.Scan(&cid, &item.name, &item.typ, &item.notNull, &value, &pk))
		if value.Valid {
			item.defaultValue = value.String
		}
		got = append(got, item)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []column{{"id", "INTEGER", "", 0}, {"owner_id", "INTEGER", "", 0}, {"task_title", "TEXT", "", 1}, {"status", "TEXT", "'open'", 1}, {"due_on", "date", "", 0}, {"owner_label", "varchar(64)", "", 1}}, got)
}

func assertTargetForeignKey(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "PRAGMA foreign_key_list('tasks')")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var id, seq int
	var table, from, to, onUpdate, onDelete, match string
	require.NoError(t, rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match))
	require.Equal(t, "parents", table)
	require.Equal(t, "owner_id", from)
	require.Equal(t, "id", to)
	require.Equal(t, "SET NULL", onUpdate)
	require.Equal(t, "CASCADE", onDelete)
	require.False(t, rows.Next())
}

func assertIndexSQL(t *testing.T, db *sql.DB, name, expected string) {
	t.Helper()
	var got string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = ?", name).Scan(&got))
	require.Equal(t, expected, got)
}

func assertUniqueIndex(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "PRAGMA index_list('"+table+"')")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var found bool
	for rows.Next() {
		var sequence int
		var name string
		var unique, partial int
		var origin string
		require.NoError(t, rows.Scan(&sequence, &name, &unique, &origin, &partial))
		if unique == 1 {
			found = true
		}
	}
	require.NoError(t, rows.Err())
	require.True(t, found)
}

func assertMissingObject(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = ?", name).Scan(&count))
	require.Equal(t, 0, count)
}

func assertMissingRebuildTables(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name LIKE 'tasks__rasql_rebuild%'").Scan(&count))
	require.Equal(t, 0, count)
}

func assertHistory(t *testing.T, db *sql.DB, table, id string, expected int) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table+" WHERE id = ?", id).Scan(&count))
	require.Equal(t, expected, count)
}
