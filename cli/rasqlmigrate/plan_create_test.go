package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

// TestRunSQLiteChangePlanCreateFlow drives "plan create" the way a user
// would: rasql schema update produces -lock against an empty database, a
// hand-written migration directory holds the pending change, and plan
// create turns the two into a plan file with no changeplan Go in sight.
// TestRunSQLiteNonemptyChangePlanFlow builds its plan by calling changeplan
// directly; this test exists to prove the CLI producer reaches the same
// place a real user's schema change would.
func TestRunSQLiteChangePlanCreateFlow(t *testing.T) {
	root := t.TempDir()
	dsn := filepath.Join(root, "application.sqlite")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	configPath := filepath.Join(root, "rasql.json")
	configBytes, err := json.Marshal(map[string]any{
		"engine":  map[string]string{"dialect": "sqlite", "profile": "sqlite-3.35"},
		"schema":  map[string]string{"kind": "live", "identity": "cli-plan-create"},
		"package": "store",
		"output":  "internal/store",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	var setupOutput, setupDiagnostics bytes.Buffer
	require.NoError(t, rasqlgen.RunTopLevelContext(t.Context(),
		[]string{"schema", "update", "-config", configPath, "-dsn", dsn}, &setupOutput, &setupDiagnostics),
		setupDiagnostics.String())
	lockPath := filepath.Join(root, "rasql.lock.json")

	migrationsDir := filepath.Join(root, "migrations", "0001_create_users")
	require.NoError(t, os.MkdirAll(migrationsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "0001.up.sql"),
		[]byte("CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY)\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "0001.down.sql"),
		[]byte("DROP TABLE users\n"), 0o600))

	planPath := filepath.Join(root, "plan.json")
	var output, diagnostics bytes.Buffer
	require.NoError(t, Run([]string{
		"plan", "create",
		"-lock", lockPath,
		"-dir", filepath.Join(root, "migrations"),
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-output", planPath,
	}, &output, &diagnostics), diagnostics.String())
	require.Equal(t, "created "+planPath+"\n", output.String())

	// plan create ran the migration for real against -dsn to observe its
	// result; undo it by hand so the database is back at -lock's baseline,
	// the same way TestRunSQLiteNonemptyChangePlanFlow drops the table it
	// creates before treating the database as a fresh apply target.
	database, err = sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "DROP TABLE users")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	// -output already exists now, and a second attempt must refuse to
	// overwrite a plan a human might already be reviewing.
	output.Reset()
	err = Run([]string{
		"plan", "create",
		"-lock", lockPath,
		"-dir", filepath.Join(root, "migrations"),
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-output", planPath,
	}, &output, &diagnostics)
	require.ErrorContains(t, err, "already exists")

	output.Reset()
	require.NoError(t, Run([]string{"plan", "-file", planPath}, &output, &diagnostics))
	require.Contains(t, output.String(), "operation\t0\t0001_create_users\tcreate_table\tengine_default\n")

	output.Reset()
	require.NoError(t, Run([]string{"plan", "check", "-file", planPath, "-dialect", "sqlite", "-dsn", dsn},
		&output, &diagnostics))
	require.Contains(t, output.String(), "next-operation\t0\n")
	require.Contains(t, output.String(), "complete\tfalse\n")

	output.Reset()
	require.NoError(t, Run([]string{"apply", "-plan", planPath, "-dialect", "sqlite", "-dsn", dsn},
		&output, &diagnostics))
	require.Equal(t, "applied-operation\t0\t0001_create_users\nmigration plan apply completed: 1 applied\n", output.String())

	database, err = sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	var tables int
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'").Scan(&tables))
	require.Equal(t, 1, tables)
}

func TestRunChangePlanCreateSelectorFlags(t *testing.T) {
	setCommandOutput(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing everything", args: []string{"plan", "create"},
			want: "plan create requires -lock, -dir, -dialect, -dsn, and -output"},
		{name: "positional", args: []string{"plan", "create", "-lock", "a", "-dir", "b", "-dialect", "sqlite",
			"-dsn", "c", "-output", "d", "extra"}, want: "plan create accepts no positional arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, run(test.args), test.want)
		})
	}
}
