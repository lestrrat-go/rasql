package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunSQLiteChangePlanCreateFlow drives "plan create" the way a user
// would: a first migration is applied for real with "migrate apply" to give
// -dsn a non-empty baseline, a second, still-pending migration directory
// holds the schema change, and plan create turns the two into a plan file --
// reading its baseline catalog live from -dsn -- with no changeplan Go in
// sight. TestRunSQLiteNonemptyChangePlanFlow builds its plan by calling
// changeplan directly; this test exists to prove the CLI producer reaches
// the same place a real user's schema change would.
func TestRunSQLiteChangePlanCreateFlow(t *testing.T) {
	root := t.TempDir()
	dsn := filepath.Join(root, "application.sqlite")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	migrationsRoot := filepath.Join(root, "migrations")
	baseDir := filepath.Join(migrationsRoot, "0000_create_accounts")
	require.NoError(t, os.MkdirAll(baseDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "0001.up.sql"),
		[]byte("CREATE TABLE accounts (id INTEGER NOT NULL PRIMARY KEY)\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "0001.down.sql"),
		[]byte("DROP TABLE accounts\n"), 0o600))

	var setupOutput, setupDiagnostics bytes.Buffer
	require.NoError(t, Run([]string{
		"apply", "-dir", migrationsRoot, "-dialect", "sqlite", "-dsn", dsn,
	}, &setupOutput, &setupDiagnostics), setupDiagnostics.String())

	migrationsDir := filepath.Join(migrationsRoot, "0001_create_users")
	require.NoError(t, os.MkdirAll(migrationsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "0001.up.sql"),
		[]byte("CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY)\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDir, "0001.down.sql"),
		[]byte("DROP TABLE users\n"), 0o600))

	planPath := filepath.Join(root, "plan.json")
	var output, diagnostics bytes.Buffer
	require.NoError(t, Run([]string{
		"plan", "create",
		"-dir", migrationsRoot,
		"-dialect", "sqlite",
		"-dsn", dsn,
		"-output", planPath,
	}, &output, &diagnostics), diagnostics.String())
	require.Equal(t, "created "+planPath+"\n", output.String())

	// plan create ran the migration for real against -dsn to observe its
	// result; undo it by hand so the database is back at its baseline, the
	// same way TestRunSQLiteNonemptyChangePlanFlow drops the table it
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
		"-dir", migrationsRoot,
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
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'accounts'").Scan(&tables))
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
			want: "plan create requires -dir, -dialect, -dsn, and -output"},
		{name: "positional", args: []string{"plan", "create", "-dir", "b", "-dialect", "sqlite",
			"-dsn", "c", "-output", "d", "extra"}, want: "plan create accepts no positional arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, run(test.args), test.want)
		})
	}
}
