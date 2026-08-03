package main

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
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

func TestRunSchemaRejectsDuplicateTableFlag(t *testing.T) {
	testCases := []struct {
		name string
		args func(input, output string) []string
	}{
		{
			name: "input",
			args: func(input, output string) []string {
				return []string{"schema", "-input", input, "-table", "users", "-table", "users", "-package", "generated", "-output", output}
			},
		},
		{
			name: "dsn",
			args: func(input, output string) []string {
				return []string{"schema", "-dsn", "postgres://example", "-table", "users", "-table", "users", "-package", "generated", "-output", output}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, os.RemoveAll(directory))
			})
			input := filepath.Join(directory, "schema.json")
			output := filepath.Join(directory, "schema.go")
			data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
			require.NoError(t, os.WriteFile(input, data, 0o600))

			database, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, mock.ExpectationsWereMet())
			})
			previousOpenDatabase := openDatabase
			openDatabase = func(driverName string, dataSourceName string) (*sql.DB, error) {
				return database, nil
			}
			t.Cleanup(func() {
				openDatabase = previousOpenDatabase
			})

			err = run(testCase.args(input, output))
			require.ErrorContains(t, err, `duplicate -table "users"`)
			_, err = os.Stat(output)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
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
	mock.ExpectQuery("SHOW server_version_num").
		WillReturnRows(sqlmock.NewRows([]string{"server_version_num"}).AddRow("180000"))
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*NOT index_metadata\\.indisvalid.*index_data\\.reloptions IS NOT NULL.*index_data\\.reltablespace <> 0.*index_metadata\\.indnullsnotdistinct.*index_metadata\\.indisreplident.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation.*attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname FROM pg_catalog\\.pg_index.*index_data\\.reloptions IS NULL.*index_data\\.reltablespace = 0.*NOT index_metadata\\.indisreplident").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL, constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}))
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

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	previousCommandOutput := commandOutput
	output := new(bytes.Buffer)
	commandOutput = output
	t.Cleanup(func() {
		commandOutput = previousCommandOutput
	})

	testCases := []struct {
		name         string
		inputName    string
		inputContent string
		buildArgs    func(input, output string) []string
	}{
		{
			name:         "schema",
			inputName:    "schema.json",
			inputContent: `[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`,
			buildArgs: func(input, output string) []string {
				return []string{"schema", "-input", input, "-package", "generated", "-output", output, "ignored", "-table", "users"}
			},
		},
		{
			name:         "query",
			inputName:    "user.sql",
			inputContent: `SELECT id FROM users WHERE id = {{bind "id"}}`,
			buildArgs: func(input, output string) []string {
				return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", output, "ignored"}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output.Reset()
			directory, err := os.MkdirTemp(".", ".tmp-unexpected-args-*")
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, os.RemoveAll(directory))
			})
			input := filepath.Join(directory, testCase.inputName)
			generated := filepath.Join(directory, "generated.go")
			require.NoError(t, os.WriteFile(input, []byte(testCase.inputContent), 0o600))

			err = run(testCase.buildArgs(input, generated))
			require.Error(t, err)
			require.ErrorContains(t, err, "unexpected arguments")
			require.NotErrorIs(t, err, flag.ErrHelp)
			_, statErr := os.Stat(generated)
			require.ErrorIs(t, statErr, os.ErrNotExist)
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

func TestRunSchemaRejectsOversizedInput(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	previousMaxInputBytes := maxInputBytes
	maxInputBytes = 128
	t.Cleanup(func() {
		maxInputBytes = previousMaxInputBytes
	})

	validInput := filepath.Join(directory, "schema.json")
	validOutput := filepath.Join(directory, "schema.go")
	validData := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.LessOrEqual(t, len(validData), maxInputBytes)
	require.NoError(t, os.WriteFile(validInput, validData, 0o600))

	require.NoError(t, run([]string{"schema", "-input", validInput, "-package", "generated", "-output", validOutput}))
	_, err = os.Stat(validOutput)
	require.NoError(t, err)

	oversizedInput := filepath.Join(directory, "oversized-schema.json")
	oversizedOutput := filepath.Join(directory, "oversized-schema.go")
	oversizedData := bytes.Repeat([]byte("x"), maxInputBytes+1)
	require.NoError(t, os.WriteFile(oversizedInput, oversizedData, 0o600))

	err = run([]string{"schema", "-input", oversizedInput, "-package", "generated", "-output", oversizedOutput})
	require.ErrorContains(t, err, oversizedInput)
	require.ErrorContains(t, err, fmt.Sprintf("%d bytes", maxInputBytes))
	_, err = os.Stat(oversizedOutput)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunQueryRejectsOversizedInput(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-query-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	previousMaxInputBytes := maxInputBytes
	maxInputBytes = 128
	t.Cleanup(func() {
		maxInputBytes = previousMaxInputBytes
	})

	validInput := filepath.Join(directory, "user.sql")
	validOutput := filepath.Join(directory, "query.go")
	validData := []byte(`SELECT id FROM users WHERE id = {{bind "id"}}`)
	require.LessOrEqual(t, len(validData), maxInputBytes)
	require.NoError(t, os.WriteFile(validInput, validData, 0o600))

	require.NoError(t, run([]string{"query", "-input", validInput, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", validOutput}))
	_, err = os.Stat(validOutput)
	require.NoError(t, err)

	oversizedInput := filepath.Join(directory, "oversized-user.sql")
	oversizedOutput := filepath.Join(directory, "oversized-query.go")
	oversizedData := bytes.Repeat([]byte("x"), maxInputBytes+1)
	require.NoError(t, os.WriteFile(oversizedInput, oversizedData, 0o600))

	err = run([]string{"query", "-input", oversizedInput, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", oversizedOutput})
	require.ErrorContains(t, err, oversizedInput)
	require.ErrorContains(t, err, fmt.Sprintf("%d bytes", maxInputBytes))
	_, err = os.Stat(oversizedOutput)
	require.ErrorIs(t, err, os.ErrNotExist)
}
