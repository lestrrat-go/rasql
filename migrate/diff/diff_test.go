package diff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/stretchr/testify/require"
)

func TestLoadSourcesOrdersNestedSQLFiles(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "tables"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "tables", "users.sql"), []byte("CREATE TABLE users (id bigint);"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "001_extensions.sql"), []byte("CREATE EXTENSION citext;"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(directory, ".cache"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".cache", "ignored.sql"), []byte("CREATE TABLE ignored (id bigint);"), 0o600))

	sources, err := diff.LoadSources(directory)
	require.NoError(t, err)
	require.Equal(t, []diff.Source{
		{Path: "001_extensions.sql", SQL: "CREATE EXTENSION citext;"},
		{Path: "tables/users.sql", SQL: "CREATE TABLE users (id bigint);"},
	}, sources)
}

func TestLoadSourcesRejectsNonSQLFiles(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "README.md"), []byte("schema"), 0o600))
	_, err := diff.LoadSources(directory)
	require.ErrorContains(t, err, "non-SQL source")
}

func TestWriteMigrationCreatesNewDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "migrations", "001_create_users")
	plan := diff.Plan{
		Dialect: "postgresql",
		Statements: []diff.PlannedStatement{
			{Source: "001_create_users.sql", SQL: "CREATE TABLE users (id bigint);\n", ReverseSQL: "DROP TABLE users;\n", Summary: "create table users"},
			{Source: "002_users_email_index.sql", SQL: "CREATE INDEX users_email_idx ON users (email);\n", ReverseSQL: "DROP INDEX users_email_idx;\n", Summary: "create index users_email_idx"},
		},
	}
	require.NoError(t, diff.WriteMigration(directory, plan))
	contents, err := os.ReadFile(filepath.Join(directory, "001_create_users.up.sql"))
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE users (id bigint);\n", string(contents))
	require.Error(t, diff.WriteMigration(directory, plan))
}

func TestPlanValidateReportsBothConflictingObjects(t *testing.T) {
	plan := diff.Plan{
		Dialect: "sqlite",
		Statements: []diff.PlannedStatement{
			{Source: "001_create_table_foo_bar.sql", SQL: "CREATE TABLE foo-bar;", ReverseSQL: "DROP TABLE foo-bar;\n", Summary: "create table foo-bar"},
			{Source: "001_create_table_foo_bar.sql", SQL: "CREATE TABLE foo_bar;", ReverseSQL: "DROP TABLE foo_bar;\n", Summary: "create table foo_bar"},
		},
	}

	require.EqualError(t, plan.Validate(), `migrate diff: duplicate generated SQL source "001_create_table_foo_bar.sql" for "create table foo-bar" and "create table foo_bar"`)
}

func TestWriteMigrationWritesIrreversibleMarker(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "migrations", "001_data_change")
	plan := diff.Plan{
		Dialect:            "sqlite",
		IrreversibleReason: "data transformation cannot be reversed",
		Statements: []diff.PlannedStatement{{
			Source: "001_transform.sql", SQL: "UPDATE users SET name = upper(name);\n", Summary: "transform users",
		}},
	}
	require.NoError(t, diff.WriteMigration(directory, plan))
	_, err := os.Stat(filepath.Join(directory, "001_transform.down.sql"))
	require.ErrorIs(t, err, os.ErrNotExist)
	contents, err := os.ReadFile(filepath.Join(directory, ".rasql-irreversible"))
	require.NoError(t, err)
	require.Equal(t, "data transformation cannot be reversed\n", string(contents))
	contents, err = os.ReadFile(filepath.Join(directory, "001_transform.up.sql"))
	require.NoError(t, err)
	require.Equal(t, "UPDATE users SET name = upper(name);\n", string(contents))
}
