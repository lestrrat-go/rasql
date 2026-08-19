package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoRunPlansAndAppliesSQLiteSQLSources(t *testing.T) {
	directory := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	migrationDirectory := filepath.Join(directory, "migrations", "001_initial")
	require.NoError(t, os.MkdirAll(migrationDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDirectory, "001_create_users.up.sql"), []byte("CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY);\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDirectory, "001_create_users.down.sql"), []byte("DROP TABLE \"users\";\n"), 0o600))
	databasePath := filepath.Join(directory, "application.db")

	plan := runCommand(t, repository, "plan", "-dir", filepath.Dir(migrationDirectory))
	require.Contains(t, plan, `CREATE TABLE "users"`)
	runCommand(t, repository, "apply", "-dir", filepath.Dir(migrationDirectory), "-dialect", "sqlite", "-dsn", databasePath)
	status := runCommand(t, repository, "status", "-dir", filepath.Dir(migrationDirectory), "-dialect", "sqlite", "-dsn", databasePath)
	require.Equal(t, "applied\t001_initial\n", status)
	runCommand(t, repository, "verify", "-dir", filepath.Dir(migrationDirectory), "-dialect", "sqlite", "-dsn", databasePath)
}

func TestGoRunHelp(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	output := runCommand(t, repository, "-h")
	require.Contains(t, output, "Usage: rasqlmigrate <command> [flags]")
}

func runCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	arguments := append([]string{"run", "./cmd/rasqlmigrate"}, args...)
	command := exec.CommandContext(t.Context(), "go", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return string(output)
}
