package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunNewCreatesMigrationDirectory(t *testing.T) {
	directory := newTestDirectory(t)
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"new", "-dir", directory, "-id", "002_add_nickname"}))
	path := filepath.Join(directory, "002_add_nickname")
	require.Equal(t, "created "+path+"; add ordered .sql files\n", outputBuffer.String())
	require.DirExists(t, path)
	require.Error(t, run([]string{"new", "-dir", directory, "-id", "002_add_nickname"}))
	require.Error(t, run([]string{"new", "-dir", directory, "-id", "../../outside"}))
	require.Error(t, run([]string{"new", "-dir", directory, "-id", ".."}))
	require.Error(t, run([]string{"new", "-dir", directory, "-id", ".hidden"}))
}

func TestRunDiffPreviewsAndWritesPostgreSQLMigration(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY, email text);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"diff", "-dialect", "postgresql", "-from", baseline, "-to", target}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())

	migrationDirectory := filepath.Join(t.TempDir(), "002_add_member_email")
	outputBuffer.Reset()
	require.NoError(t, run([]string{"diff", "-dialect", "postgresql", "-from", baseline, "-to", target, "-output", migrationDirectory}))
	require.Equal(t, "created "+migrationDirectory+"\n", outputBuffer.String())
	contents, err := os.ReadFile(filepath.Join(migrationDirectory, "001_add_column_members_email.sql"))
	require.NoError(t, err)
	require.Equal(t, "ALTER TABLE members ADD COLUMN email text;\n", string(contents))
}

func TestRunDiffPreviewsMySQLMigration(t *testing.T) {
	baseline := filepath.Join(t.TempDir(), "baseline")
	target := filepath.Join(t.TempDir(), "target")
	writeTestSchema(t, baseline, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY);\n")
	writeTestSchema(t, target, "tables/members.sql", "CREATE TABLE members (id bigint PRIMARY KEY, email text);\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"diff", "-dialect", "mysql", "-from", baseline, "-to", target}))
	require.Equal(t, "-- 001_add_column_members_email.sql: add column members.email\nALTER TABLE members ADD COLUMN email text;\n", outputBuffer.String())

	migrationDirectory := filepath.Join(t.TempDir(), "002_add_member_email")
	outputBuffer.Reset()
	require.NoError(t, run([]string{"diff", "-dialect", "mysql", "-from", baseline, "-to", target, "-output", migrationDirectory}))
	require.Equal(t, "created "+migrationDirectory+"\n", outputBuffer.String())
	contents, err := os.ReadFile(filepath.Join(migrationDirectory, "001_add_column_members_email.sql"))
	require.NoError(t, err)
	require.Equal(t, "ALTER TABLE members ADD COLUMN email text;\n", string(contents))
}

func TestRunPlanPrintsSQLSources(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestSQL(t, directory, "001_create_users", "001_create_users.sql", "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY);\n")
	writeTestSQL(t, directory, "001_create_users", "002_users_email_index.sql", "CREATE INDEX \"users_email_idx\" ON \"users\" (\"email\");\n")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"plan", "-dir", directory}))
	require.Equal(t, "-- 001_create_users/001_create_users.sql\nCREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY);\n\n-- 001_create_users/002_users_email_index.sql\nCREATE INDEX \"users_email_idx\" ON \"users\" (\"email\");\n", outputBuffer.String())
}

func TestRunApplyStatusAndVerifySQLiteSQLSources(t *testing.T) {
	directory := newTestDirectory(t)
	writeTestSQL(t, directory, "001_create_users", "001_create_users.sql", "CREATE TABLE \"users\" (\"id\" INTEGER PRIMARY KEY);\n")
	dsn := filepath.Join(t.TempDir(), "application.db")
	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"apply", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "migration apply completed\n", outputBuffer.String())

	outputBuffer.Reset()
	require.NoError(t, run([]string{"status", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "applied\t001_create_users\n", outputBuffer.String())

	outputBuffer.Reset()
	require.NoError(t, run([]string{"verify", "-dir", directory, "-dialect", "sqlite", "-dsn", dsn}))
	require.Equal(t, "migration verification passed\n", outputBuffer.String())
}

func TestLoadMigrationsRejectsUnexpectedEntries(t *testing.T) {
	directory := newTestDirectory(t)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "001_create_users.sql"), []byte("CREATE TABLE users (id INTEGER);"), 0o600))
	_, err := loadMigrations(directory)
	require.ErrorContains(t, err, "non-directory entry")

	require.NoError(t, os.Remove(filepath.Join(directory, "001_create_users.sql")))
	writeTestSQL(t, directory, "001_create_users", "001_create_users.txt", "CREATE TABLE users (id INTEGER);")
	_, err = loadMigrations(directory)
	require.ErrorContains(t, err, "non-SQL source")
}

func TestRunHelpAndUnknownCommand(t *testing.T) {
	outputBuffer := setCommandOutput(t)
	err := run([]string{"-h"})
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, outputBuffer.String(), "Usage: rasqlmigrate <command> [flags]")
	require.Error(t, run([]string{"unknown"}))
}

func TestRedactErrorRemovesDSN(t *testing.T) {
	dsn := "postgres://user:secret@example.test/database"
	err := redactError(errors.New("connect "+dsn+" failed"), dsn)
	require.NotContains(t, err.Error(), dsn)
	require.Contains(t, err.Error(), "[redacted]")
}

func newTestDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".tmp-rasqlmigrate-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	return directory
}

func setCommandOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := commandOutput
	output := new(bytes.Buffer)
	commandOutput = output
	t.Cleanup(func() {
		commandOutput = previous
	})
	return output
}

func writeTestSQL(t *testing.T, directory string, migrationID string, filename string, source string) {
	t.Helper()
	migrationDirectory := filepath.Join(directory, migrationID)
	require.NoError(t, os.MkdirAll(migrationDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(migrationDirectory, filename), []byte(source), 0o600))
}

func writeTestSchema(t *testing.T, directory string, filename string, source string) {
	t.Helper()
	path := filepath.Join(directory, filename)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
}
