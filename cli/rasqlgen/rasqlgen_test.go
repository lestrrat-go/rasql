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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/stretchr/testify/require"
)

var commandOutput io.Writer = os.Stderr

func run(args []string) error {
	return Run(args, commandOutput)
}

func TestRunSchemaRejectsDuplicateTableFlag(t *testing.T) {
	directory := t.TempDir()

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

	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-table", "users", "-package", "generated", "-output", directory})
	require.ErrorContains(t, err, `duplicate -table "users"`)
	_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func queryOutputArgs(t *testing.T, directory string) []string {
	t.Helper()
	input := filepath.Join(directory, "user.sql")
	require.NoError(t, os.WriteFile(input, []byte(`SELECT id FROM users WHERE id = {{bind "id"}}`), 0o600))
	return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated"}
}

func TestRunQueryRejectsDirectoryOutput(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "outdir")
	require.NoError(t, os.Mkdir(output, 0o700))

	err := run(append(queryOutputArgs(t, directory), "-output", output))
	require.ErrorContains(t, err, "is a directory")
	require.Empty(t, mustReadDir(t, output))
}

func TestRunQueryOutputPaths(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing", "out_gen.go")
	err := run(append(queryOutputArgs(t, directory), "-output", missing))
	require.Error(t, err)

	output := filepath.Join(directory, "out_gen.go")
	require.NoError(t, run(append(queryOutputArgs(t, directory), "-output", output)))
	require.FileExists(t, output)
}

func TestRunQueryRejectsOutputWithoutGeneratedSuffix(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "output.go")

	err := run(append(queryOutputArgs(t, directory), "-output", output))
	require.ErrorContains(t, err, "must end in _gen.go")
	require.NoFileExists(t, output)
}

// handWrittenSource is a file somebody wrote by hand that happens to sit
// where generated output would land. Nothing about it says rasqlgen, which
// is the whole point: the destination must survive.
const handWrittenSource = "package store\n\n// Written by hand. Losing this file loses the only copy.\nfunc Keep() bool { return true }\n"

// TestRunQueryRefusesHandWrittenDestination confirms the query command
// reaches the same guard, since it writes through the same door.
func TestRunQueryRefusesHandWrittenDestination(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "user_gen.go")
	require.NoError(t, os.WriteFile(output, []byte(handWrittenSource), 0o600))

	err := run(append(queryOutputArgs(t, directory), "-output", output))
	require.ErrorContains(t, err, "refusing to overwrite")
	require.ErrorContains(t, err, output)
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, handWrittenSource, string(source))
}

// TestGeneratedOutputCarriesTheMarker holds each family's marker constant
// in internal/genfile to what the generators actually emit. genfile decides
// what it may replace by that first line, while internal/schemagen and
// template each write the line themselves, so a generator that changed its
// header would otherwise make every regeneration refuse the output of the
// run before it. Running schema, query, and bootstrap covers every place a
// marker line is written.
func TestGeneratedOutputCarriesTheMarker(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	require.NoError(t, run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "generated", "-output", directory}))

	queryOutput := filepath.Join(directory, "user_by_id_gen.go")
	require.NoError(t, run(append(queryOutputArgs(t, directory), "-output", queryOutput)))

	schemaWritten := []string{"users_gen.go", "schema_gen.go", "schema_gen_test.go"}
	for _, name := range schemaWritten {
		source, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(source), genfile.Schema.Marker()+"\n"),
			"%s must start with %q, which is what rasqlgen recognizes its own schema output by", name, genfile.Schema.Marker())
	}

	querySource, err := os.ReadFile(queryOutput)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(querySource), genfile.Query.Marker()+"\n"),
		"user_by_id_gen.go must start with %q, which is what rasqlgen recognizes its own query output by", genfile.Query.Marker())

	bootstrapDirectory := t.TempDir()
	require.NoError(t, run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "tables", "-output", bootstrapDirectory}))

	bootstrapWritten := []string{"users_gen.go", "tables_gen.go"}
	for _, name := range bootstrapWritten {
		source, err := os.ReadFile(filepath.Join(bootstrapDirectory, name))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(source), genfile.Bootstrap.Marker()+"\n"),
			"%s must start with %q, which is what rasqlgen recognizes its own bootstrap output by", name, genfile.Bootstrap.Marker())
	}

	hintsSource, err := os.ReadFile(filepath.Join(bootstrapDirectory, "hints.go"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(hintsSource), "// Code generated by rasqlgen bootstrap once; edit freely.\n"),
		"hints.go must start with the hints header, which is what tells editors and linters it is meant to be hand-edited")
}

func mustReadDir(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	return entries
}

func TestRunSchemaInspectsPostgreSQL(t *testing.T) {
	directory := t.TempDir()
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
	mock.ExpectQuery("SELECT column_data\\.column_name, column_data\\.data_type, column_data\\.is_nullable, column_data\\.column_default, column_data\\.numeric_precision, column_data\\.numeric_scale, column_data\\.character_maximum_length, column_data\\.is_generated, column_data\\.generation_expression, attribute\\.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	mock.ExpectQuery("SELECT count\\(attribute\\.attnum\\) FROM pg_catalog\\.pg_class AS table_data.*JOIN pg_catalog\\.pg_namespace AS table_namespace.*LEFT JOIN pg_catalog\\.pg_attribute AS attribute.*table_data\\.relkind IN \\('r','p','v','f'\\) GROUP BY table_data\\.oid").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname", "key_expression", "amname", "predicate", "descending", "operator_class", "collation", "include_columns", "not_valid", "storage_parameters", "tablespace", "replica_identity", "nulls_not_distinct", "nulls_first"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, \\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\), constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory})
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "func Users() UsersTable {")
	descriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), "var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}")
	// inspect never reports a namespace, so the generated descriptor must not
	// qualify the table with one.
	require.NotContains(t, string(descriptor), "Schema:")
}

func TestRunSchemaInspectsSQLite(t *testing.T) {
	directory := t.TempDir()
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
	descriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), `Schema: "main"`)
}

func TestRunSchemaInspectsMySQL(t *testing.T) {
	directory := t.TempDir()
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
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", ""))
	mock.ExpectQuery("SHOW CREATE TABLE `users`").
		WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).
			AddRow("users", "CREATE TABLE `users` (`id` bigint NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB"))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints JOIN information_schema\\.key_column_usage").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery("SELECT key_column_usage\\.constraint_name, key_column_usage\\.column_name, FALSE, FALSE, FALSE, NULL, FALSE, NULL, NULL, FALSE, NULL FROM information_schema\\.table_constraints JOIN information_schema\\.key_column_usage.*constraint_type = 'UNIQUE'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT check_constraints\\.constraint_name, check_constraints\\.check_clause, FALSE, TRUE, table_constraints\\.enforced = 'YES' FROM information_schema\\.check_constraints JOIN information_schema\\.table_constraints.*constraint_type = 'CHECK'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}))
	mock.ExpectQuery("SHOW COLUMNS FROM information_schema.statistics LIKE 'EXPRESSION'").
		WillReturnRows(sqlmock.NewRows([]string{"Field"}).AddRow("EXPRESSION"))
	mock.ExpectQuery("SHOW COLUMNS FROM information_schema.statistics LIKE 'IS_VISIBLE'").
		WillReturnRows(sqlmock.NewRows([]string{"Field"}).AddRow("IS_VISIBLE"))
	mock.ExpectQuery("SELECT index_name, non_unique = 0, column_name, sub_part, expression, collation, index_type, is_visible FROM information_schema\\.statistics.*index_name <> 'PRIMARY'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))
	mock.ExpectQuery("SELECT key_column_usage\\.constraint_name, key_column_usage\\.column_name, key_column_usage\\.referenced_table_name, key_column_usage\\.referenced_column_name, CASE referential_constraints\\.delete_rule.*referenced_table_name IS NOT NULL").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = run([]string{"schema", "-dsn", "rasql:rasql@tcp(localhost:3306)/rasql", "-dialect", "mysql", "-table", "users", "-package", "generated", "-output", directory})
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "type UsersRow struct {")
	require.NotContains(t, string(source), "Schema:")
	require.Contains(t, string(source), "ID int64")
}

func TestRunSchemaRejectsNonPositiveTimeout(t *testing.T) {
	timeouts := []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0s"},
		{name: "negative", value: "-5s"},
	}

	for _, timeout := range timeouts {
		t.Run(timeout.name, func(t *testing.T) {
			directory := t.TempDir()

			err := run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory, "-timeout", timeout.value})
			require.ErrorContains(t, err, "schema -timeout must be positive")
			_, err = os.Stat(filepath.Join(directory, "users_gen.go"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestRunSchemaInspectionRespectsTimeout(t *testing.T) {
	directory := t.TempDir()
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
	directory := t.TempDir()
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
	directory := t.TempDir()

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

	err := run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", directory})
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
	directory := t.TempDir()
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
			name:     "bootstrap",
			args:     []string{"bootstrap", "-h"},
			expected: "Usage of bootstrap:",
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
				return []string{"schema", "-dsn", "postgres://example", "-package", "generated", "-output", output, "ignored", "-table", "users"}
			},
			expected: `unexpected arguments: ["ignored" "-table" "users"]`,
		},
		{
			name:         "schema with an empty argument",
			inputName:    "schema.json",
			inputContent: schemaContent,
			buildArgs: func(input, output string) []string {
				return []string{"schema", "-dsn", "postgres://example", "-package", "generated", "-output", output, ""}
			},
			expected: `unexpected arguments: [""]`,
		},
		{
			name:         "schema with an argument holding a space",
			inputName:    "schema.json",
			inputContent: schemaContent,
			buildArgs: func(input, output string) []string {
				return []string{"schema", "-dsn", "postgres://example", "-package", "generated", "-output", output, "one two"}
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
			directory := t.TempDir()
			input := filepath.Join(directory, testCase.inputName)
			generated := filepath.Join(directory, "generated_gen.go")
			require.NoError(t, os.WriteFile(input, []byte(testCase.inputContent), 0o600))

			err := run(testCase.buildArgs(input, generated))
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

func TestRunQueryRejectsOversizedInput(t *testing.T) {
	directory := t.TempDir()
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
	_, err := os.Stat(validOutput)
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
