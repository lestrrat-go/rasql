package rasqlmigrate

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSchemaEvolutionCLIDiffLiveSQLiteRefusesTemporaryNameExhaustion(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT);")
	require.NoError(t, err)
	for index := 0; index < 1000; index++ {
		name := "tasks__rasql_rebuild"
		if index > 0 {
			name = fmt.Sprintf("tasks__rasql_rebuild_%d", index+1)
		}
		_, err = database.ExecContext(t.Context(), "CREATE TABLE \""+name+"\" (id INTEGER);")
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, target, "tables/tasks.sql", "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT NOT NULL);\n")
	parent := filepath.Join(t.TempDir(), "missing")
	output := filepath.Join(parent, "001_refused")
	err = run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "tasks", "-to", target, "-output", output})
	require.EqualError(t, err, "sqlite schema diff: table tasks has no available rebuild temporary name")
	assertNoCLIOutputPath(t, parent, "001_refused")
}

func TestSchemaEvolutionCLIDiffLiveSQLiteRefusesTriggerAndView(t *testing.T) {
	for _, test := range []struct {
		name, objectSQL, want string
	}{
		{"trigger", "CREATE TRIGGER tasks_audit AFTER INSERT ON tasks BEGIN SELECT 1; END;", "sqlite schema diff: rebuild has unsafe live dependencies: [tasks_audit]"},
		{"view", "CREATE VIEW task_view AS SELECT id FROM tasks;", "sqlite schema diff: rebuild has unsafe live dependencies: [task_view]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "application.db")
			database, err := sql.Open("sqlite", dsn)
			require.NoError(t, err)
			_, err = database.ExecContext(t.Context(), "CREATE TABLE tasks (id INTEGER PRIMARY KEY);"+test.objectSQL)
			require.NoError(t, err)
			require.NoError(t, database.Close())
			target := filepath.Join(t.TempDir(), "target")
			writeTestSchema(t, target, "tables/tasks.sql", "CREATE TABLE tasks (id INTEGER PRIMARY KEY, label TEXT);\n")
			parent := filepath.Join(t.TempDir(), "missing")
			err = run([]string{"diff-live", "-dialect", "sqlite", "-dsn", dsn, "-table", "tasks", "-to", target, "-output", filepath.Join(parent, "001_refused")})
			require.EqualError(t, err, test.want)
			assertNoCLIOutputPath(t, parent, "001_refused")
		})
	}
}

func TestSchemaEvolutionCLIDiffSQLiteRefusesGeneratedColumns(t *testing.T) {
	for _, test := range []struct {
		name, baseline, target string
	}{
		{"baseline", "CREATE TABLE tasks (id INTEGER, value INTEGER GENERATED ALWAYS AS (id * 2));\n", "CREATE TABLE tasks (id INTEGER, value INTEGER GENERATED ALWAYS AS (id * 3));\n"},
		{"target", "CREATE TABLE tasks (id INTEGER);\n", "CREATE TABLE tasks (id INTEGER, value INTEGER GENERATED ALWAYS AS (id * 3));\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := filepath.Join(t.TempDir(), "baseline")
			target := filepath.Join(t.TempDir(), "target")
			writeTestSchema(t, baseline, "tables/tasks.sql", test.baseline)
			writeTestSchema(t, target, "tables/tasks.sql", test.target)
			parent := filepath.Join(t.TempDir(), "missing")
			err := run([]string{"diff", "-dialect", "sqlite", "-from", baseline, "-to", target, "-output", filepath.Join(parent, "001_refused")})
			require.EqualError(t, err, `sqlite schema diff: rebuild table tasks cannot represent generated column "value"`)
			assertNoCLIOutputPath(t, parent, "001_refused")
		})
	}
}
