package sqlite_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestDiffNumbersLargePlan(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE existing (id INTEGER PRIMARY KEY);")
	var source strings.Builder
	for index := 0; index < 1000; index++ {
		source.WriteString("CREATE TABLE table_")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" (id INTEGER PRIMARY KEY); ")
	}
	target := parseSnapshot(t, analyzer, source.String()+"CREATE TABLE existing (id INTEGER PRIMARY KEY);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 1000)
	sources := make([]string, len(plan.Statements))
	for index, statement := range plan.Statements {
		sources[index] = statement.Source
	}
	sorted := append([]string(nil), sources...)
	sort.Strings(sorted)
	require.Equal(t, sources, sorted)
	require.Equal(t, "0001_", sources[0][:5])
	require.Equal(t, "1000_", sources[len(sources)-1][:5])
}

func TestSchemaEvolutionSQLiteRebuildPopulatedRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", "file:rebuild-populated?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), `PRAGMA foreign_keys = ON;
CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, title TEXT NOT NULL,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE);
CREATE INDEX tasks_title_idx ON tasks(title);
INSERT INTO parents VALUES (1), (2);
INSERT INTO tasks VALUES (10, 1, 'one'), (20, 2, 'two');`)
	require.NoError(t, err)
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, title TEXT NOT NULL,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE);
CREATE INDEX tasks_title_idx ON tasks(title);`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, title TEXT NOT NULL,
  due_on DATE, owner_label TEXT NOT NULL,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE);
CREATE INDEX tasks_title_idx ON tasks(title);`)
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Decisions, 1)
	require.Empty(t, plan.Statements)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-' || owner_id;"})
	require.NoError(t, err)
	second, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-' || owner_id;"})
	require.NoError(t, err)
	require.Equal(t, resolved.Statements, second.Statements)
	require.NotEmpty(t, resolved.Statements)
	require.Len(t, resolved.Operations, 1)
	operation := resolved.Operations[0]
	require.Equal(t, resolved.Statements, operation.Forward)
	for index, reverse := range operation.Reverse {
		forward := resolved.Statements[len(resolved.Statements)-1-index]
		require.Equal(t, forward.Source, reverse.Source)
		require.Equal(t, forward.ReverseSQL, reverse.SQL)
		require.Equal(t, forward.SQL, reverse.ReverseSQL)
	}
	for _, statement := range resolved.Statements {
		require.NotEmpty(t, statement.ReverseSQL, statement.Source)
	}
	var reverseSQL string
	for _, statement := range resolved.Statements {
		reverseSQL += statement.ReverseSQL
	}
	require.Contains(t, reverseSQL, "tasks__rasql_rebuild_2")
	require.Contains(t, reverseSQL, "CREATE TABLE")
	require.Contains(t, reverseSQL, "INSERT INTO tasks__rasql_rebuild_2")
	migration := migrate.Migration{ID: "001_rebuild", Statements: make([]migrate.Statement, len(resolved.Statements)), Down: make([]migrate.Statement, len(resolved.Statements))}
	for i, statement := range resolved.Statements {
		migration.Statements[i] = migrate.Statement{Source: statement.Source, SQL: sqltext.Text(statement.SQL)}
		migration.Down[len(resolved.Statements)-1-i] = migrate.Statement{Source: statement.Source, SQL: sqltext.Text(statement.ReverseSQL)}
	}
	runner, err := migrate.New(db, dialect.SQLite())
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migration)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM tasks WHERE owner_label LIKE 'owner-%'").Scan(&count))
	require.Equal(t, 2, count)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), migration)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM tasks").Scan(&count))
	require.Equal(t, 2, count)
}

func TestSchemaEvolutionSQLiteRebuildRollsBackEverySource(t *testing.T) {
	db, err := sql.Open("sqlite", "file:rebuild-rollback?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), `PRAGMA foreign_keys = ON;
CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, label TEXT,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL);
CREATE INDEX tasks_label_idx ON tasks(label);
INSERT INTO parents VALUES (1);
INSERT INTO tasks VALUES (1, 1, 'same'), (2, 1, 'same');`)
	require.NoError(t, err)
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE parents (id INTEGER PRIMARY KEY); CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, label TEXT, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL); CREATE INDEX tasks_label_idx ON tasks(label);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE parents (id INTEGER PRIMARY KEY); CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, label TEXT UNIQUE, owner_label TEXT NOT NULL, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL); CREATE INDEX tasks_label_idx ON tasks(label); CREATE INDEX tasks_owner_idx ON tasks(owner_id);")
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner-' || owner_id;"})
	require.NoError(t, err)
	output := t.TempDir()
	require.NoError(t, diff.WriteMigration(filepath.Join(output, "001_rollback"), resolved))
	loaded, err := migrationdir.Load(output)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	runner, err := migrate.NewWithHistoryTable(db, dialect.SQLite(), "schema_history")
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), loaded[0])
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM tasks").Scan(&count))
	require.Equal(t, 2, count)
	rows, err := db.QueryContext(t.Context(), "SELECT id, owner_id, label FROM tasks ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for id := 1; id <= 2; id++ {
		require.True(t, rows.Next())
		var gotID, gotOwner int
		var gotLabel string
		require.NoError(t, rows.Scan(&gotID, &gotOwner, &gotLabel))
		require.Equal(t, id, gotID)
		require.Equal(t, 1, gotOwner)
		require.Equal(t, "same", gotLabel)
	}
	require.False(t, rows.Next())
	var schemaSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name = 'tasks'").Scan(&schemaSQL))
	require.Equal(t, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, label TEXT,\n  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL)", schemaSQL)
	fkRows, err := db.QueryContext(t.Context(), "PRAGMA foreign_key_list('tasks')")
	require.NoError(t, err)
	defer func() { _ = fkRows.Close() }()
	require.True(t, fkRows.Next())
	var fkID, fkSeq int
	var fkTable, fkFrom, fkTo, fkUpdate, fkDelete, fkMatch string
	require.NoError(t, fkRows.Scan(&fkID, &fkSeq, &fkTable, &fkFrom, &fkTo, &fkUpdate, &fkDelete, &fkMatch))
	require.Equal(t, "parents", fkTable)
	require.Equal(t, "owner_id", fkFrom)
	require.Equal(t, "id", fkTo)
	require.Equal(t, "SET NULL", fkUpdate)
	require.Equal(t, "CASCADE", fkDelete)
	require.False(t, fkRows.Next())
	var ownerID int
	var label string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT owner_id, label FROM tasks WHERE id = 2").Scan(&ownerID, &label))
	require.Equal(t, 1, ownerID)
	require.Equal(t, "same", label)
	var indexSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name = 'tasks_label_idx'").Scan(&indexSQL))
	require.Equal(t, "CREATE INDEX tasks_label_idx ON tasks(label)", indexSQL)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'tasks_owner_idx'").Scan(&count))
	require.Equal(t, 0, count)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'schema_history'").Scan(&count))
	require.Equal(t, 0, count)
	var temporaryCount int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name LIKE 'tasks__rasql_rebuild%'").Scan(&temporaryCount))
	require.Equal(t, 0, temporaryCount)
}

func TestSchemaEvolutionSQLiteRebuildRefusesUnsafeConsumers(t *testing.T) {
	tests := []struct {
		name             string
		baseline, target string
		facts            *sqlite.LiveCatalogFacts
		want             string
	}{
		{"baseline generated", "CREATE TABLE tasks (id INTEGER, value INTEGER GENERATED ALWAYS AS (id * 2));", "CREATE TABLE tasks (id INTEGER, value INTEGER GENERATED ALWAYS AS (id * 3));", nil, "cannot represent generated column"},
		{"target generated", "CREATE TABLE tasks (id INTEGER);", "CREATE TABLE tasks (id INTEGER, value INTEGER GENERATED ALWAYS AS (id * 3));", nil, "cannot represent generated column"},
		{"trigger", "CREATE TABLE tasks (id INTEGER);", "CREATE TABLE tasks (id INTEGER, label TEXT);", &sqlite.LiveCatalogFacts{TriggerNames: []string{"tasks_audit"}}, "unsafe live dependencies"},
		{"view", "CREATE TABLE tasks (id INTEGER);", "CREATE TABLE tasks (id INTEGER, label TEXT);", &sqlite.LiveCatalogFacts{ViewNames: []string{"task_view"}}, "unsafe live dependencies"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analyzer := sqlite.New()
			baseline := parseSnapshot(t, analyzer, test.baseline)
			target := parseSnapshot(t, analyzer, test.target)
			if test.facts != nil {
				var err error
				_, err = analyzer.AttachLiveCatalog(baseline, *test.facts)
				require.ErrorContains(t, err, test.want)
				return
			}
			_, err := analyzer.Diff(baseline, target)
			require.ErrorContains(t, err, test.want)
		})
	}

	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, label TEXT);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, label TEXT, owner_label TEXT NOT NULL);")
	baseline, err := analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	invalid := []struct{ name, sql, want string }{
		{"wrong table", "UPDATE other SET owner_label = 'x';", "source targets table"},
		{"other column", "UPDATE tasks SET label = 'x';", "assign only column"},
		{"multiple statements", "UPDATE tasks SET owner_label = 'x'; UPDATE tasks SET owner_label = 'y';", "multiple statements"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			resolved, resolveErr := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: test.sql})
			require.ErrorContains(t, resolveErr, test.want)
			require.Empty(t, resolved.Statements)
			output := filepath.Join(t.TempDir(), "nested", "001")
			require.Error(t, diff.WriteMigration(output, resolved))
			_, statErr := os.Stat(filepath.Dir(output))
			require.Error(t, statErr)
		})
	}
}

func TestSchemaEvolutionSQLiteCombinedArtifactsAndCatalog(t *testing.T) {
	db, err := sql.Open("sqlite", "file:rebuild-combined?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), `PRAGMA foreign_keys = ON;
CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, title TEXT NOT NULL,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE);
CREATE INDEX tasks_title_idx ON tasks(title);
INSERT INTO parents VALUES (1), (2);
INSERT INTO tasks VALUES (10, 1, 'one'), (20, 2, 'two');`)
	require.NoError(t, err)
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL, title TEXT NOT NULL,
  CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE);
CREATE INDEX tasks_title_idx ON tasks(title);`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE parents (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, owner_id INTEGER, task_title TEXT NOT NULL, due_on DATE,
  owner_label VARCHAR(64) NOT NULL, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents(id) ON DELETE CASCADE,
  CONSTRAINT tasks_title_unique UNIQUE (task_title));
CREATE INDEX tasks_owner_idx ON tasks(owner_id);`)
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Equal(t, diff.OperationRebuildTable, plan.Operations[0].Kind)
	resolutions := make([]diff.Resolution, 0, len(plan.Decisions))
	for _, decision := range plan.Decisions {
		resolution := diff.Resolution{DecisionID: decision.ID}
		if decision.Kind == diff.DecisionRename {
			resolution.RenameFrom = decision.Baseline
		} else {
			resolution.BackfillSQL = "UPDATE tasks SET owner_label = 'owner-' || owner_id;"
		}
		resolutions = append(resolutions, resolution)
	}
	resolved, err := plan.Resolve(resolutions...)
	require.NoError(t, err)
	require.Len(t, resolved.Operations, 1)
	output := t.TempDir()
	require.NoError(t, diff.WriteMigration(filepath.Join(output, "001_combined"), resolved))
	loaded, err := migrationdir.Load(output)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	runner, err := migrate.NewWithHistoryTable(db, dialect.SQLite(), "schema_history")
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), loaded[0])
	require.NoError(t, err)
	var history int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM schema_history WHERE id = '001_combined'").Scan(&history))
	require.Equal(t, 1, history)
	var title, ownerLabel string
	rows, err := db.QueryContext(t.Context(), "SELECT task_title, owner_id, due_on, owner_label FROM tasks ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for _, expected := range []struct {
		title, owner string
		ownerID      int
	}{{"one", "owner-1", 1}, {"two", "owner-2", 2}} {
		var ownerID int
		var due sql.NullString
		require.True(t, rows.Next())
		require.NoError(t, rows.Scan(&title, &ownerID, &due, &ownerLabel))
		require.Equal(t, expected.title, title)
		require.Equal(t, expected.owner, ownerLabel)
		require.Equal(t, expected.ownerID, ownerID)
		require.False(t, due.Valid)
	}
	require.False(t, rows.Next())
	var tableSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name = 'tasks'").Scan(&tableSQL))
	require.Equal(t, "CREATE TABLE \"tasks\" (id integer PRIMARY KEY, owner_id integer, task_title text NOT NULL, due_on date, owner_label varchar(64) NOT NULL, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents (id) ON DELETE CASCADE, CONSTRAINT tasks_title_unique UNIQUE (task_title))", tableSQL)
	var columns int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_table_info('tasks') WHERE name IN ('id', 'owner_id', 'task_title', 'due_on', 'owner_label')").Scan(&columns))
	require.Equal(t, 5, columns)
	var indexes int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_index_list('tasks') WHERE name = 'tasks_owner_idx'").Scan(&indexes))
	require.Equal(t, 1, indexes)
	var indexSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name = 'tasks_owner_idx'").Scan(&indexSQL))
	require.Equal(t, "CREATE INDEX tasks_owner_idx ON tasks (owner_id)", indexSQL)
	var fkRows *sql.Rows
	fkRows, err = db.QueryContext(t.Context(), "PRAGMA foreign_key_list('tasks')")
	require.NoError(t, err)
	require.True(t, fkRows.Next())
	var fkID, fkSeq int
	var fkTable, fkFrom, fkTo, fkUpdate, fkDelete, fkMatch string
	require.NoError(t, fkRows.Scan(&fkID, &fkSeq, &fkTable, &fkFrom, &fkTo, &fkUpdate, &fkDelete, &fkMatch))
	require.Equal(t, "parents", fkTable)
	require.Equal(t, "owner_id", fkFrom)
	require.Equal(t, "id", fkTo)
	require.Equal(t, "CASCADE", fkDelete)
	require.False(t, fkRows.Next())
	require.NoError(t, fkRows.Close())
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name LIKE 'tasks__rasql_rebuild%'").Scan(&columns))
	require.Equal(t, 0, columns)
	_, err = runner.Revert(t.Context(), migrate.Steps(1), loaded[0])
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM schema_history WHERE id = '001_combined'").Scan(&history))
	require.Equal(t, 0, history)
	var restored string
	rows, err = db.QueryContext(t.Context(), "SELECT title, owner_id FROM tasks ORDER BY id")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for _, expected := range []struct {
		title string
		owner int
	}{{"one", 1}, {"two", 2}} {
		var owner int
		require.True(t, rows.Next())
		require.NoError(t, rows.Scan(&restored, &owner))
		require.Equal(t, expected.title, restored)
		require.Equal(t, expected.owner, owner)
	}
	require.False(t, rows.Next())
	require.Error(t, db.QueryRowContext(t.Context(), "SELECT task_title FROM tasks").Scan(&restored))
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'tasks_title_idx'").Scan(&columns))
	require.Equal(t, 1, columns)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'tasks_owner_idx'").Scan(&columns))
	require.Equal(t, 0, columns)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name = 'tasks'").Scan(&tableSQL))
	require.Equal(t, "CREATE TABLE \"tasks\" (id integer PRIMARY KEY, owner_id integer NOT NULL, title text NOT NULL, CONSTRAINT tasks_owner_fk FOREIGN KEY (owner_id) REFERENCES parents (id) ON DELETE CASCADE)", tableSQL)
}

func TestSchemaEvolutionSQLiteGlobalArtifactNumbering(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, `CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);`)
	target := parseSnapshot(t, analyzer, `CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, owner_label TEXT NOT NULL);
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, owner_label TEXT NOT NULL);
CREATE TABLE audit (id INTEGER PRIMARY KEY);`)
	baseline, err := analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	resolutions := make([]diff.Resolution, 0, len(plan.Decisions))
	for _, decision := range plan.Decisions {
		resolutions = append(resolutions, diff.Resolution{DecisionID: decision.ID, BackfillSQL: "UPDATE " + decision.Table + " SET " + decision.Column + " = 'owner';"})
	}
	resolved, err := plan.Resolve(resolutions...)
	require.NoError(t, err)
	require.Len(t, resolved.Operations, 3)
	seen := make(map[string]struct{})
	expectedSources := []string{
		"001_create_table_audit.sql",
		"002_0_stage_owner_label.sql", "003_1_backfill_owner_label.sql", "004_2_create_rebuild.sql",
		"005_3_copy_rebuild.sql", "006_4_drop_table.sql", "007_5_rename_rebuild.sql",
		"008_0_stage_owner_label.sql", "009_1_backfill_owner_label.sql", "010_2_create_rebuild.sql",
		"011_3_copy_rebuild.sql", "012_4_drop_table.sql", "013_5_rename_rebuild.sql",
	}
	expectedSQL := []string{
		"CREATE TABLE audit (id integer PRIMARY KEY);\n",
		"ALTER TABLE tasks ADD COLUMN owner_label text;\n", "UPDATE tasks SET owner_label = 'owner';\n",
		"CREATE TABLE tasks__rasql_rebuild (id integer PRIMARY KEY, name text, owner_label text NOT NULL);\n\n",
		"INSERT INTO tasks__rasql_rebuild (id, name, owner_label) SELECT id, name, owner_label FROM tasks;\n",
		"DROP TABLE tasks;\n", "ALTER TABLE tasks__rasql_rebuild RENAME TO tasks;\n",
		"ALTER TABLE users ADD COLUMN owner_label text;\n", "UPDATE users SET owner_label = 'owner';\n",
		"CREATE TABLE users__rasql_rebuild (id integer PRIMARY KEY, name text, owner_label text NOT NULL);\n\n",
		"INSERT INTO users__rasql_rebuild (id, name, owner_label) SELECT id, name, owner_label FROM users;\n",
		"DROP TABLE users;\n", "ALTER TABLE users__rasql_rebuild RENAME TO users;\n",
	}
	expectedReverseSQL := []string{
		"DROP TABLE audit;\n", "-- staging column is removed by the structural rebuild\n", "-- backfill is removed by the structural rebuild\n",
		"ALTER TABLE tasks__rasql_rebuild_2 RENAME TO tasks;\n", "DROP TABLE tasks;\n",
		"INSERT INTO tasks__rasql_rebuild_2 (id, name) SELECT id, name FROM tasks;\n", "CREATE TABLE tasks__rasql_rebuild_2 (id integer PRIMARY KEY, name text);\n\n",
		"-- staging column is removed by the structural rebuild\n", "-- backfill is removed by the structural rebuild\n",
		"ALTER TABLE users__rasql_rebuild_2 RENAME TO users;\n", "DROP TABLE users;\n",
		"INSERT INTO users__rasql_rebuild_2 (id, name) SELECT id, name FROM users;\n", "CREATE TABLE users__rasql_rebuild_2 (id integer PRIMARY KEY, name text);\n\n",
	}
	for index, statement := range resolved.Statements {
		require.Equal(t, expectedSources[index], statement.Source)
		require.Equal(t, expectedSQL[index], statement.SQL)
		require.Equal(t, expectedReverseSQL[index], statement.ReverseSQL)
		require.NotContains(t, seen, statement.Source)
		seen[statement.Source] = struct{}{}
	}
	require.Equal(t, []diff.OperationKind{diff.OperationCreateTable, diff.OperationRebuildTable, diff.OperationRebuildTable}, []diff.OperationKind{resolved.Operations[0].Kind, resolved.Operations[1].Kind, resolved.Operations[2].Kind})
	require.Equal(t, []string{"audit", "tasks", "users"}, []string{resolved.Operations[0].Table, resolved.Operations[1].Table, resolved.Operations[2].Table})
	statementIndex := 0
	for _, operation := range resolved.Operations {
		require.NotEmpty(t, operation.Forward)
		require.Len(t, operation.Reverse, len(operation.Forward))
		for index, forward := range operation.Forward {
			require.Equal(t, resolved.Statements[statementIndex], forward)
			require.Equal(t, forward.Source, operation.Reverse[len(operation.Reverse)-1-index].Source)
			require.Equal(t, forward.ReverseSQL, operation.Reverse[len(operation.Reverse)-1-index].SQL)
			require.Equal(t, forward.SQL, operation.Reverse[len(operation.Reverse)-1-index].ReverseSQL)
			statementIndex++
		}
	}
	require.Equal(t, len(resolved.Statements), statementIndex)
	root := t.TempDir()
	require.NoError(t, diff.WriteMigration(filepath.Join(root, "001_combined"), resolved))
	loaded, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, loaded[0].Statements, len(resolved.Statements))
	for index, statement := range loaded[0].Statements {
		require.Equal(t, strings.TrimSuffix(expectedSources[index], ".sql")+".up.sql", statement.Source)
		require.Equal(t, expectedSQL[index], string(statement.SQL))
	}
	require.Len(t, loaded[0].Down, len(resolved.Statements))
	for index, statement := range loaded[0].Down {
		forwardIndex := len(expectedSources) - 1 - index
		require.Equal(t, strings.TrimSuffix(expectedSources[forwardIndex], ".sql")+".down.sql", statement.Source)
		require.Equal(t, expectedReverseSQL[forwardIndex], string(statement.SQL))
	}
}

func TestSchemaEvolutionSQLiteRebuildPreservesDefaultAndCollation(t *testing.T) {
	db, err := sql.Open("sqlite", "file:rebuild-default?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO tasks VALUES (1, 'one');")
	require.NoError(t, err)
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, status TEXT COLLATE NOCASE DEFAULT 'new', owner_label VARCHAR(64) COLLATE NOCASE NOT NULL);")
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner';"})
	require.NoError(t, err)
	var stageSQL string
	for _, statement := range resolved.Statements {
		if strings.HasSuffix(statement.Source, "_stage_owner_label.sql") {
			stageSQL = statement.SQL
		}
	}
	require.Contains(t, strings.ToUpper(stageSQL), "VARCHAR(64)")
	require.Contains(t, strings.ToUpper(stageSQL), "COLLATE NOCASE")
	require.NotContains(t, strings.ToUpper(stageSQL), "NOT NULL")
	migration := migrate.Migration{ID: "001_default", Statements: make([]migrate.Statement, len(resolved.Statements))}
	for i, statement := range resolved.Statements {
		migration.Statements[i] = migrate.Statement{Source: statement.Source, SQL: sqltext.Text(statement.SQL)}
	}
	runner, err := migrate.New(db, dialect.SQLite())
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migration)
	require.NoError(t, err)
	var status, schemaSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT status FROM tasks WHERE id = 1").Scan(&status))
	require.Equal(t, "new", status)
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name = 'tasks'").Scan(&schemaSQL))
	require.Contains(t, strings.ToUpper(schemaSQL), "COLLATE NOCASE")
}

func TestSchemaEvolutionSQLiteRebuildPreservesNestedDefaults(t *testing.T) {
	db, err := sql.Open("sqlite", "file:rebuild-nested-default?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE tasks (id INTEGER PRIMARY KEY); INSERT INTO tasks VALUES (1);")
	require.NoError(t, err)
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, note TEXT DEFAULT 'a,b', score INTEGER DEFAULT (1 + (2 * 3)), owner_label TEXT NOT NULL);")
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner';"})
	require.NoError(t, err)
	var copySQL string
	for _, statement := range resolved.Statements {
		if strings.HasSuffix(statement.Source, "_copy_rebuild.sql") {
			copySQL = statement.SQL
		}
	}
	require.Contains(t, copySQL, "'a,b'")
	require.Contains(t, copySQL, "(1 + (2 * 3))")
	migration := migrate.Migration{ID: "001_nested", Statements: make([]migrate.Statement, len(resolved.Statements))}
	for i, statement := range resolved.Statements {
		migration.Statements[i] = migrate.Statement{Source: statement.Source, SQL: sqltext.Text(statement.SQL)}
	}
	runner, err := migrate.New(db, dialect.SQLite())
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migration)
	require.NoError(t, err)
	var note string
	var score int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT note, score FROM tasks WHERE id = 1").Scan(&note, &score))
	require.Equal(t, "a,b", note)
	require.Equal(t, 7, score)
}

func TestSchemaEvolutionSQLiteGroupsOneTableRebuild(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, label TEXT);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, label TEXT UNIQUE, due_on DATE);")
	baseline, err := analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Equal(t, diff.OperationRebuildTable, plan.Operations[0].Kind)
	require.NotEmpty(t, plan.Statements)
	for i, statement := range plan.Statements {
		require.NotContains(t, statement.SQL, "BEGIN")
		require.NotContains(t, statement.SQL, "COMMIT")
		if i > 0 {
			require.NotEqual(t, plan.Statements[i-1].Source, statement.Source)
		}
	}
}

func TestSchemaEvolutionSQLiteRebuildRefusesGeneratedColumn(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, value INTEGER, doubled INTEGER GENERATED ALWAYS AS (value * 2));")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, value INTEGER, doubled INTEGER GENERATED ALWAYS AS (value * 3), due_on DATE);")
	baseline, err := analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	_, err = analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "cannot represent generated column")
}

func TestDiffWriteMigrationLoadsExactArtifact(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id INTEGER PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT); CREATE INDEX members_email_idx ON members (email);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, diff.WriteMigration(filepath.Join(root, "001_add_email"), plan))
	loaded, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Len(t, loaded[0].Statements, len(plan.Statements))
	for index, statement := range plan.Statements {
		require.Equal(t, statement.Source[:len(statement.Source)-4]+".up.sql", loaded[0].Statements[index].Source)
		require.Equal(t, statement.SQL, string(loaded[0].Statements[index].SQL))
	}
	require.Len(t, loaded[0].Down, len(plan.Statements))
	for index, statement := range plan.Statements {
		require.Equal(t, statement.Source[:len(statement.Source)-4]+".down.sql", loaded[0].Down[len(plan.Statements)-index-1].Source)
		require.Equal(t, statement.ReverseSQL, string(loaded[0].Down[len(plan.Statements)-index-1].SQL))
	}
}

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

// TestLiveSourcesRejectsVirtualTable is the VirtualTableModule counterpart
// to TestLiveSourcesRejectsStrictTable.
func TestLiveSourcesRejectsVirtualTable(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:                        "posts_fts",
		Columns:                     []schema.ColumnDef{{Name: "body", Type: schema.TextType{}, Nullable: true}},
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"body"},
	})
	require.ErrorContains(t, err, `"posts_fts"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestLiveSourcesRejectsUniqueKeyDetails is the UniqueDef.Keys counterpart
// to TestLiveSourcesRejectsStrictTable.
func TestLiveSourcesRejectsUniqueKeyDetails(t *testing.T) {
	analyzer := sqlite.New()
	_, err := analyzer.LiveSources(schema.TableDef{
		Name:       "members",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}}},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{Keys: []schema.IndexKeyDef{{Expression: "email", Descending: true}}},
		},
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
				Source:     "001_add_column_members_email.sql",
				SQL:        "ALTER TABLE members ADD COLUMN email text;\n",
				ReverseSQL: "ALTER TABLE members DROP COLUMN email;\n",
				Summary:    "add column members.email",
			},
			{
				Source:     "002_create_index_members_email_idx.sql",
				SQL:        "CREATE INDEX members_email_idx ON members (email);\n",
				ReverseSQL: "DROP INDEX members_email_idx;\n",
				Summary:    "create index members_email_idx",
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
		Source:     "001_create_table_projects.sql",
		SQL:        "CREATE TABLE projects (id integer PRIMARY KEY, owner_id integer NOT NULL);\n",
		ReverseSQL: "DROP TABLE projects;\n",
		Summary:    "create table projects",
	}}, plan.Statements)
}

// TestLiveSourcesRejectsGeneratedColumn proves that an inspected table
// carrying a generated column does not reach diff-live's generated
// desired-schema sources as a silently downgraded plain writable column:
// LiveSources renders through render.CreateTable, which refuses
// GeneratedExpression, so the error surfaces here rather than a Plan going
// on to emit DDL for a column that cannot be written to at all.
func TestLiveSourcesRejectsGeneratedColumn(t *testing.T) {
	analyzer := sqlite.New()
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

func TestDiffRejectsCollidingGeneratedStatementNames(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, `CREATE TABLE members (id integer PRIMARY KEY); CREATE TABLE "foo-bar" (id integer PRIMARY KEY); CREATE TABLE foo_bar (id integer PRIMARY KEY);`)

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Statements, 2)
	require.Contains(t, plan.Statements[0].SQL, "foo-bar")
	require.Contains(t, plan.Statements[1].SQL, "foo_bar")
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

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Empty(t, plan.Statements)
	require.False(t, plan.Executable())
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

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, "backfill_sqlite_members_email", plan.Decisions[0].ID)
}

func TestDiffProposesConfirmedCompatibleRename(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (name text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (display_name text);")
	baseline, err := analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{})
	require.NoError(t, err)
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Decisions, 1)
	require.Equal(t, diff.DecisionRename, plan.Decisions[0].Kind)
	require.Equal(t, "name", plan.Decisions[0].Baseline)
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, RenameFrom: "name"})
	require.NoError(t, err)
	require.Contains(t, resolved.Statements[1].SQL, "SELECT name FROM members")
	require.Contains(t, resolved.Statements[2].ReverseSQL, "SELECT display_name FROM members")
}

func TestDiffRefusesIncompatibleRenameCandidate(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (name text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (display_name integer);")
	_, err := analyzer.Diff(baseline, target)
	require.Error(t, err)
}

func TestDiffRefusesAmbiguousRenameCandidates(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (first text, second text);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (given text, family text);")
	_, err := analyzer.Diff(baseline, target)
	require.Error(t, err)
}

func TestDiffKeepsSQLiteSupportedOperationBesideRequiredColumn(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY, due_on text, email text NOT NULL);")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
	require.Len(t, plan.Operations, 1)
	require.Equal(t, diff.OperationRebuildTable, plan.Operations[0].Kind)
	require.Equal(t, "members", plan.Operations[0].Table)
	require.Empty(t, plan.Operations[0].Column)
	require.Equal(t, "rebuild_table_sqlite_members", plan.Operations[0].ID)
	require.Len(t, plan.Decisions, 1)
	require.Equal(t, diff.DecisionBackfill, plan.Decisions[0].Kind)
	require.Equal(t, "members", plan.Decisions[0].Table)
	require.Equal(t, "email", plan.Decisions[0].Column)
	require.Equal(t, "required column needs an application-specific backfill", plan.Decisions[0].Reason)
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
			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			require.Len(t, plan.Operations, 1)
			require.Empty(t, plan.Statements)
			require.False(t, plan.Executable())
		})
	}
}

func TestDiffRejectsChangedOptions(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE members (id integer PRIMARY KEY) STRICT;")

	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Empty(t, plan.Statements)
	require.False(t, plan.Executable())
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
			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			require.Len(t, plan.Operations, 1)
			require.Empty(t, plan.Statements)
			require.False(t, plan.Executable())
		})
	}
}

func TestSQLitePrimaryKeyNullabilityMatchesLiveSQLite(t *testing.T) {
	tests := []struct {
		name         string
		baseline     string
		target       string
		insert       string
		rejectsNull  bool
		targetReject bool
		expectsEqual bool
	}{
		{name: "ordinary inline text key", baseline: "CREATE TABLE members (id TEXT PRIMARY KEY);", target: "CREATE TABLE members (id TEXT PRIMARY KEY NOT NULL);", insert: "INSERT INTO members (id) VALUES (NULL);", targetReject: true},
		{name: "ordinary table text key", baseline: "CREATE TABLE members (id TEXT, PRIMARY KEY (id));", target: "CREATE TABLE members (id TEXT NOT NULL, PRIMARY KEY (id));", insert: "INSERT INTO members (id) VALUES (NULL);", targetReject: true},
		{name: "ordinary composite key", baseline: "CREATE TABLE members (a TEXT, b TEXT, PRIMARY KEY (a, b));", target: "CREATE TABLE members (a TEXT NOT NULL, b TEXT NOT NULL, PRIMARY KEY (a, b));", insert: "INSERT INTO members (a, b) VALUES (NULL, 'b');", targetReject: true},
		{name: "integer rowid alias", baseline: "CREATE TABLE members (id INTEGER PRIMARY KEY);", target: "CREATE TABLE members (id INTEGER, PRIMARY KEY (id));", insert: "INSERT INTO members (id) VALUES (NULL);", expectsEqual: true},
		{name: "inline integer descending key", baseline: "CREATE TABLE members (id INTEGER PRIMARY KEY DESC);", target: "CREATE TABLE members (id INTEGER PRIMARY KEY);", insert: "INSERT INTO members (id) VALUES (NULL);"},
		{name: "strict text key", baseline: "CREATE TABLE members (id TEXT PRIMARY KEY) STRICT;", target: "CREATE TABLE members (id TEXT PRIMARY KEY NOT NULL) STRICT;", insert: "INSERT INTO members (id) VALUES (NULL);", rejectsNull: true, expectsEqual: true},
		{name: "without rowid composite key", baseline: "CREATE TABLE members (a TEXT, b TEXT, PRIMARY KEY (a, b)) WITHOUT ROWID;", target: "CREATE TABLE members (a TEXT NOT NULL, b TEXT NOT NULL, PRIMARY KEY (a, b)) WITHOUT ROWID;", insert: "INSERT INTO members (a, b) VALUES (NULL, 'b');", rejectsNull: true, expectsEqual: true},
		{name: "non-key not null", baseline: "CREATE TABLE members (id TEXT PRIMARY KEY, name TEXT NOT NULL);", target: "CREATE TABLE members (id TEXT PRIMARY KEY, name TEXT NOT NULL);", insert: "INSERT INTO members (id, name) VALUES ('id', 'name');", expectsEqual: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analyzer := sqlite.New()
			baseline := parseSnapshot(t, analyzer, test.baseline)
			target := parseSnapshot(t, analyzer, test.target)
			plan, err := analyzer.Diff(baseline, target)
			require.NoError(t, err)
			if test.expectsEqual {
				require.Empty(t, plan.Operations)
				require.Empty(t, plan.Statements)
			} else {
				require.NotEmpty(t, plan.Operations)
			}

			for index, source := range []string{test.baseline, test.target} {
				database, err := sql.Open("sqlite", ":memory:")
				require.NoError(t, err)
				_, err = database.ExecContext(t.Context(), source)
				require.NoError(t, err)
				_, err = database.ExecContext(t.Context(), test.insert)
				rejectsNull := test.rejectsNull || index == 1 && test.targetReject
				if rejectsNull {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NoError(t, database.Close())
			}
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
	snapshot, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text(source)}})
	require.NoError(t, err)
	return snapshot
}
