package rasqlgen

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

var commandOutput io.Writer = os.Stderr

func run(args []string) error {
	return Run(args, commandOutput)
}

func TestRunSchemaGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", directory}))
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Users() UsersTable {")
}

// TestRunSchemaKeepsUnsignedColumnsFromInput proves signedness survives the
// one path that carries a descriptor as JSON. An unsigned integer column read
// through -input has to reach the generator still unsigned: dropped there, it
// would generate an int64 field and a signed descriptor, and the column would
// silently lose every value above 9223372036854775807.
func TestRunSchemaKeepsUnsignedColumnsFromInput(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[{"Name":"events","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":true}},{"Name":"sequence","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", directory}))
	source, err := os.ReadFile(filepath.Join(directory, "events_gen.go"))
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+uint64$`, string(source))
	require.Regexp(t, `(?m)^\s*Sequence\s+int64$`, string(source))
	require.Contains(t, string(source), `{Name: "id", Type: schema.IntegerType{Unsigned: true}},`)
}

func TestRunSchemaFiltersInputTables(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[
		{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]},
		{"Name":"orders","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}
	]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-table", "users", "-package", "generated", "-output", directory}))
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.NoFileExists(t, filepath.Join(directory, "orders_gen.go"))
}

func TestRunSchemaRejectsDuplicateFilteredInputTables(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[
		{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]},
		{"Name":"users","Columns":[{"Name":"email","Type":{"Kind":"text"}}],"PrimaryKey":["email"]}
	]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-table", "users", "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, `generate: table "users" duplicates generated name "Users"`)
	_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunSchemaRejectsUnknownInputTable(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-table", "orders", "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, `schema input has no table "orders"`)
	_, err = os.Stat(filepath.Join(directory, "orders_gen.go"))
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
			data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
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

			err = run(testCase.args(input, directory))
			require.ErrorContains(t, err, `duplicate -table "users"`)
			_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestRunSchemaGeneratesOneFilePerTable(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[
		{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]},
		{"Name":"orders","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}
	]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", directory}))
	users, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	orders, err := os.ReadFile(filepath.Join(directory, "orders_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(users), "func Users() UsersTable {")
	require.NotContains(t, string(users), "func Orders() OrdersTable {")
	require.Contains(t, string(orders), "func Orders() OrdersTable {")
	require.NotContains(t, string(orders), "func Users() UsersTable {")
}

func TestRunSchemaRejectsCrossFileNameCollisionsBeforeWriting(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[
		{"Name":"api_keys","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]},
		{"Name":"api__keys","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}
	]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, `generate: table "api_keys" duplicates generated name "APIKeys"`)
	require.NoFileExists(t, filepath.Join(directory, "api_keys_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "api__keys_gen.go"))
}

// TestRunSchemaSuccessLeavesNoTemporaryFile confirms that a successful run
// leaves each final generated file, without a temporary file from its
// write-then-rename sequence.
func TestRunSchemaSuccessLeavesNoTemporaryFile(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	require.NoError(t, run([]string{"schema", "-input", input, "-package", "generated", "-output", directory}))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.ElementsMatch(t, []string{"schema.json", "users_gen.go"}, names)
}

// TestRunSchemaOutputDirectoryMissingReturnsError confirms that schema output
// must name an existing directory.
func TestRunSchemaOutputDirectoryMissingReturnsError(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "missing")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(directory, "users_gen.go"))
}

func TestRunSchemaRejectsFileOutput(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "schema_gen.go")
	require.NoError(t, os.WriteFile(input, []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`), 0o600))
	require.NoError(t, os.WriteFile(output, []byte("SENTINEL"), 0o600))

	err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})
	require.ErrorContains(t, err, "is not a directory")
	contents, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, []byte("SENTINEL"), contents)
}

func queryOutputArgs(t *testing.T, directory string) []string {
	t.Helper()
	input := filepath.Join(directory, "user.sql")
	require.NoError(t, os.WriteFile(input, []byte(`SELECT id FROM users WHERE id = {{bind "id"}}`), 0o600))
	return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated"}
}

func TestRunQueryRejectsDirectoryOutput(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-output-directory-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	output := filepath.Join(directory, "outdir")
	require.NoError(t, os.Mkdir(output, 0o700))

	err = run(append(queryOutputArgs(t, directory), "-output", output))
	require.ErrorContains(t, err, "is a directory")
	require.Empty(t, mustReadDir(t, output))
}

func TestRunQueryOutputPaths(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-output-directory-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	missing := filepath.Join(directory, "missing", "out_gen.go")
	err = run(append(queryOutputArgs(t, directory), "-output", missing))
	require.Error(t, err)

	output := filepath.Join(directory, "out_gen.go")
	require.NoError(t, run(append(queryOutputArgs(t, directory), "-output", output)))
	require.FileExists(t, output)
}

func TestRunQueryRejectsOutputWithoutGeneratedSuffix(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-output-name-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	output := filepath.Join(directory, "output.go")

	err = run(append(queryOutputArgs(t, directory), "-output", output))
	require.ErrorContains(t, err, "must end in _gen.go")
	require.NoFileExists(t, output)
}

func mustReadDir(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	return entries
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
	mock.ExpectBegin()
	mock.ExpectQuery("SHOW server_version_num").
		WillReturnRows(sqlmock.NewRows([]string{"server_version_num"}).AddRow("180000"))
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil))
	mock.ExpectQuery("SELECT count\\(attribute\\.attnum\\) FROM pg_catalog\\.pg_class AS table_data.*JOIN pg_catalog\\.pg_namespace AS table_namespace.*LEFT JOIN pg_catalog\\.pg_attribute AS attribute.*table_data\\.relkind IN \\('r','p','v','f'\\) GROUP BY table_data\\.oid").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
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
	mock.ExpectCommit()
	mock.ExpectClose()

	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory})
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Users() UsersTable {")
}

func TestRunSchemaInspectsSQLite(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	databasePath := filepath.Join(directory, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	require.NoError(t, err)

	err = run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "generated", "-output", directory})
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "type UsersRow struct {")
	require.Contains(t, string(source), "ID   int64")
	require.Contains(t, string(source), "Name string")
}

func TestRunSchemaInspectsMySQL(t *testing.T) {
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
		require.Equal(t, "mysql", driverName)
		require.Equal(t, "rasql:rasql@tcp(localhost:3306)/rasql", dataSourceName)
		return database, nil
	}
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
	})
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil))
	mock.ExpectQuery("SHOW CREATE TABLE `users`").
		WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).
			AddRow("users", "CREATE TABLE `users` (`id` bigint NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB"))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints JOIN information_schema\\.key_column_usage").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = run([]string{"schema", "-dsn", "rasql:rasql@tcp(localhost:3306)/rasql", "-dialect", "mysql", "-table", "users", "-package", "generated", "-output", directory})
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "type UsersRow struct {")
	require.Contains(t, string(source), "ID int64")
}

func TestRunSchemaRejectsNonPositiveTimeout(t *testing.T) {
	sources := []struct {
		name string
		args func(input, output string) []string
	}{
		{
			name: "input",
			args: func(input, output string) []string {
				return []string{"schema", "-input", input, "-package", "generated", "-output", output}
			},
		},
		{
			name: "dsn",
			args: func(input, output string) []string {
				return []string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", output}
			},
		},
	}
	timeouts := []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0s"},
		{name: "negative", value: "-5s"},
	}

	for _, source := range sources {
		for _, timeout := range timeouts {
			t.Run(source.name+"/"+timeout.name, func(t *testing.T) {
				directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, os.RemoveAll(directory))
				})
				input := filepath.Join(directory, "schema.json")
				data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
				require.NoError(t, os.WriteFile(input, data, 0o600))

				err = run(append(source.args(input, directory), "-timeout", timeout.value))
				require.ErrorContains(t, err, "schema -timeout must be positive")
				_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}

func TestRunSchemaInspectionRespectsTimeout(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		// database/sql rolls the transaction back from its own goroutine once
		// the deadline fires, so the driver may see Rollback after run
		// returns. Poll instead of asserting synchronously.
		require.Eventually(t, func() bool {
			return mock.ExpectationsWereMet() == nil
		}, time.Second, 5*time.Millisecond)
	})
	previousOpenDatabase := openDatabase
	openDatabase = func(driverName string, dataSourceName string) (*sql.DB, error) {
		return database, nil
	}
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
	})
	// inspectionDelay must stay well above the -timeout below and is reused
	// verbatim as the elapsed-time assertion bound, so the test proves the
	// query was cut short rather than merely completing quickly.
	const inspectionDelay = 500 * time.Millisecond
	mock.ExpectBegin()
	mock.ExpectQuery("SHOW server_version_num").
		WillDelayFor(inspectionDelay).
		WillReturnRows(sqlmock.NewRows([]string{"server_version_num"}).AddRow("180000"))
	mock.ExpectRollback()
	mock.ExpectClose()

	start := time.Now()
	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-timeout", "20ms", "-package", "generated", "-output", directory})
	elapsed := time.Since(start)
	// Keep the full prefix, not just the cancellation tail, so this cannot be
	// satisfied by a cancellation raised at some other query.
	require.ErrorContains(t, err, "read PostgreSQL server version: canceling query due to user request")
	// Bound against the delay constant itself, not a smaller round number:
	// observed elapsed is ~21ms against a 500ms delay, a ~24x margin. A
	// tighter bound (e.g. 50ms) would add no discrimination and could flake
	// on a loaded machine.
	require.Less(t, elapsed, inspectionDelay, "inspection must be cut short by -timeout, took %s", elapsed)
	_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunSchemaRejectsTransactionBeginFailure(t *testing.T) {
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
		return database, nil
	}
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
	})
	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
	mock.ExpectClose()

	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, "begin PostgreSQL inspection transaction")
	require.ErrorContains(t, err, "connection refused")
	_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// recordingTxDriver is a minimal driver.Driver that records the
// driver.TxOptions its single connection receives through ConnBeginTx.
// sqlmock does not expose driver.TxOptions, so this drives the assertion
// that rasqlgen opens its inspection transaction as repeatable-read,
// read-only.
type recordingTxDriver struct {
	recorded chan driver.TxOptions
}

func (d *recordingTxDriver) Open(name string) (driver.Conn, error) {
	return &recordingTxConn{driver: d}, nil
}

type recordingTxConn struct {
	driver *recordingTxDriver
}

func (c *recordingTxConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("recordingTxConn: Prepare not supported")
}

func (c *recordingTxConn) Close() error {
	return nil
}

func (c *recordingTxConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recordingTxConn: Begin not supported, expected BeginTx")
}

func (c *recordingTxConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.driver.recorded <- opts
	return recordingTxTx{}, nil
}

type recordingTxTx struct{}

func (recordingTxTx) Commit() error   { return nil }
func (recordingTxTx) Rollback() error { return nil }

var recordingTxDriverCounter int64

func TestRunSchemaBeginsRepeatableReadReadOnlyTransaction(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})

	driverName := fmt.Sprintf("rasqlgen-recording-tx-driver-%d", atomic.AddInt64(&recordingTxDriverCounter, 1))
	recorder := &recordingTxDriver{recorded: make(chan driver.TxOptions, 1)}
	sql.Register(driverName, recorder)

	previousOpenDatabase := openDatabase
	openDatabase = func(driverNameArgument string, dataSourceName string) (*sql.DB, error) {
		return sql.Open(driverName, dataSourceName)
	}
	t.Cleanup(func() {
		openDatabase = previousOpenDatabase
	})

	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory})
	require.Error(t, err)

	select {
	case opts := <-recorder.recorded:
		require.Equal(t, sql.LevelRepeatableRead, sql.IsolationLevel(opts.Isolation))
		require.True(t, opts.ReadOnly)
	default:
		t.Fatal("BeginTx was not called")
	}
	_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunQueryGeneratesSource(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-query-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "user.sql")
	output := filepath.Join(directory, "query_gen.go")
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

func TestRunRejectsArgumentsAfterHelp(t *testing.T) {
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
			name:     "schema",
			args:     []string{"schema", "-h", "ignored"},
			expected: `unexpected arguments: ["ignored"]`,
		},
		{
			name:     "schema with several arguments",
			args:     []string{"schema", "-h", "ignored", "more"},
			expected: `unexpected arguments: ["ignored" "more"]`,
		},
		{
			name:     "query",
			args:     []string{"query", "-h", "ignored"},
			expected: `unexpected arguments: ["ignored"]`,
		},
		{
			name:     "query with several arguments",
			args:     []string{"query", "-h", "ignored", "more"},
			expected: `unexpected arguments: ["ignored" "more"]`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output.Reset()
			err := run(testCase.args)
			require.EqualError(t, err, testCase.expected)
			// main exits 0 on flag.ErrHelp, so a help error here would
			// swallow the leftover argument.
			require.NotErrorIs(t, err, flag.ErrHelp)
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

	const schemaContent = `[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`
	const queryContent = `SELECT id FROM users WHERE id = {{bind "id"}}`

	testCases := []struct {
		name         string
		inputName    string
		inputContent string
		buildArgs    func(input, output string) []string
		expected     string
	}{
		{
			name:         "schema",
			inputName:    "schema.json",
			inputContent: schemaContent,
			buildArgs: func(input, output string) []string {
				return []string{"schema", "-input", input, "-package", "generated", "-output", output, "ignored", "-table", "users"}
			},
			expected: `unexpected arguments: ["ignored" "-table" "users"]`,
		},
		{
			name:         "schema with an empty argument",
			inputName:    "schema.json",
			inputContent: schemaContent,
			buildArgs: func(input, output string) []string {
				return []string{"schema", "-input", input, "-package", "generated", "-output", output, ""}
			},
			expected: `unexpected arguments: [""]`,
		},
		{
			name:         "schema with an argument holding a space",
			inputName:    "schema.json",
			inputContent: schemaContent,
			buildArgs: func(input, output string) []string {
				return []string{"schema", "-input", input, "-package", "generated", "-output", output, "one two"}
			},
			expected: `unexpected arguments: ["one two"]`,
		},
		{
			name:         "query",
			inputName:    "user.sql",
			inputContent: queryContent,
			buildArgs: func(input, output string) []string {
				return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", output, "ignored"}
			},
			expected: `unexpected arguments: ["ignored"]`,
		},
		{
			name:         "query with an empty argument",
			inputName:    "user.sql",
			inputContent: queryContent,
			buildArgs: func(input, output string) []string {
				return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", output, ""}
			},
			expected: `unexpected arguments: [""]`,
		},
		{
			name:         "query with an argument holding a space",
			inputName:    "user.sql",
			inputContent: queryContent,
			buildArgs: func(input, output string) []string {
				return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", output, "one two"}
			},
			expected: `unexpected arguments: ["one two"]`,
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
			generated := filepath.Join(directory, "generated_gen.go")
			require.NoError(t, os.WriteFile(input, []byte(testCase.inputContent), 0o600))

			err = run(testCase.buildArgs(input, generated))
			require.EqualError(t, err, testCase.expected)
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
	validData := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false}}],"PrimaryKey":["id"]}]`)
	require.LessOrEqual(t, len(validData), maxInputBytes)
	require.NoError(t, os.WriteFile(validInput, validData, 0o600))

	require.NoError(t, run([]string{"schema", "-input", validInput, "-package", "generated", "-output", directory}))
	_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)

	oversizedInput := filepath.Join(directory, "oversized-schema.json")
	oversizedData := bytes.Repeat([]byte("x"), maxInputBytes+1)
	require.NoError(t, os.WriteFile(oversizedInput, oversizedData, 0o600))

	err = run([]string{"schema", "-input", oversizedInput, "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, oversizedInput)
	require.ErrorContains(t, err, fmt.Sprintf("%d bytes", maxInputBytes))
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
	validOutput := filepath.Join(directory, "query_gen.go")
	validData := []byte(`SELECT id FROM users WHERE id = {{bind "id"}}`)
	require.LessOrEqual(t, len(validData), maxInputBytes)
	require.NoError(t, os.WriteFile(validInput, validData, 0o600))

	require.NoError(t, run([]string{"query", "-input", validInput, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", validOutput}))
	_, err = os.Stat(validOutput)
	require.NoError(t, err)

	oversizedInput := filepath.Join(directory, "oversized-user.sql")
	oversizedOutput := filepath.Join(directory, "oversized-query_gen.go")
	oversizedData := bytes.Repeat([]byte("x"), maxInputBytes+1)
	require.NoError(t, os.WriteFile(oversizedInput, oversizedData, 0o600))

	err = run([]string{"query", "-input", oversizedInput, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated", "-output", oversizedOutput})
	require.ErrorContains(t, err, oversizedInput)
	require.ErrorContains(t, err, fmt.Sprintf("%d bytes", maxInputBytes))
	_, err = os.Stat(oversizedOutput)
	require.ErrorIs(t, err, os.ErrNotExist)
}
