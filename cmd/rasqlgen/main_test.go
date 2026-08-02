package main

import (
	"bytes"
	"database/sql"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestRunSchemaGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", output}))
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Users() UsersTable {")
}

func TestRunSchemaFiltersInputTables(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[
		{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]},
		{"Name":"orders","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}
	]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-table", "users", "-package", "generated", "-output", output}))
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.NotContains(t, string(source), "func Orders() OrdersTable {")
}

func TestRunSchemaRejectsDuplicateFilteredInputTables(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[
		{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]},
		{"Name":"users","Columns":[{"Name":"email","Type":"text"}],"PrimaryKey":["email"]}
	]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-table", "users", "-package", "generated", "-output", output})
	require.ErrorContains(t, err, `generate: table "users" duplicates generated name "Users"`)
	_, err = os.Stat(output)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunSchemaRejectsUnknownInputTable(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-table", "orders", "-package", "generated", "-output", output})
	require.ErrorContains(t, err, `schema input has no table "orders"`)
	_, err = os.Stat(output)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunSchemaInspectsPostgreSQL(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
	})
	previousOpenDatabase := openDatabase
	openDatabase = func(driverName string, dataSourceName string) (*sql.DB, error) {
		require.Equal(t, "pgx", driverName)
		require.Equal(t, "postgres://example", dataSourceName)
		return database, nil
	}
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
	})
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*index_metadata\\.indnullsnotdistinct.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns"}))
	mock.ExpectClose()

	output := filepath.Join(directory, "schema.go")
	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", output})
	require.NoError(t, err)
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Users() UsersTable {")
}

func TestRunQueryGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-query-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "user.sql")
	output := filepath.Join(directory, "query.go")
	require.NoError(t, os.WriteFile(input, []byte("SELECT id FROM users WHERE id = {{bind \"id\"}}"), 0o600))

	require.NoError(t, run([]string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", output}))
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "func UserByID(id any)")
	require.Contains(t, string(source), "id = $1")
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	require.Error(t, run([]string{"unknown"}))
}

func TestRunHelp(t *testing.T) {
	previousCommandOutput := commandOutput
	output := new(bytes.Buffer)
	commandOutput = output
	t.Cleanup(func() {
		commandOutput = previousCommandOutput
	})

	testCases := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "global",
			args:     []string{"-h"},
			expected: "Usage: rasqlgen <command> [flags]",
		},
		{
			name:     "schema",
			args:     []string{"schema", "-h"},
			expected: "Usage of schema:",
		},
		{
			name:     "query",
			args:     []string{"query", "-h"},
			expected: "Usage of query:",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output.Reset()
			err := run(testCase.args)
			require.ErrorIs(t, err, flag.ErrHelp)
			require.Contains(t, output.String(), testCase.expected)
		})
	}
}

func TestRunRejectsInvalidFlag(t *testing.T) {
	previousCommandOutput := commandOutput
	output := new(bytes.Buffer)
	commandOutput = output
	t.Cleanup(func() {
		commandOutput = previousCommandOutput
	})

	err := run([]string{"schema", "-unknown"})
	require.Error(t, err)
	require.NotErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, output.String(), "flag provided but not defined: -unknown")
}
