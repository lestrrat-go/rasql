package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

// TestRunSchemaSuccessLeavesNoTemporaryFile confirms that a successful run
// cleans up after itself: only the input and the final output file remain
// in the output directory, with no leftover temporary file from the
// write-then-rename sequence.
func TestRunSchemaSuccessLeavesNoTemporaryFile(t *testing.T) {
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

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.ElementsMatch(t, []string{"schema.json", "schema.go"}, names)
}

// TestRunSchemaOutputDirectoryMissingReturnsError confirms that an -output
// path inside a nonexistent directory surfaces as an error, since
// writeGeneratedFile's os.CreateTemp call can fail before its cleanup
// defer is registered.
func TestRunSchemaOutputDirectoryMissingReturnsError(t *testing.T) {
	directory, err := os.MkdirTemp(".", ".tmp-schema-command-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	input := filepath.Join(directory, "schema.json")
	output := filepath.Join(directory, "missing", "schema.go")
	data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	err = run([]string{"schema", "-input", input, "-package", "generated", "-output", output})
	require.Error(t, err)
}

// outputCommandFixture names one of rasqlgen's two subcommands together
// with the -input fixture it needs, so a test that must run the same
// -output check against both commands can do so from one table instead of
// duplicating the test body per command. write creates whatever -input
// fixture the command needs inside directory and returns the argument list
// up to, but not including, -output.
type outputCommandFixture struct {
	name  string
	write func(t *testing.T, directory string) []string
}

var outputCommandFixtures = []outputCommandFixture{
	{
		name: "schema",
		write: func(t *testing.T, directory string) []string {
			t.Helper()
			input := filepath.Join(directory, "schema.json")
			data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
			require.NoError(t, os.WriteFile(input, data, 0o600))
			return []string{"schema", "-input", input, "-package", "generated"}
		},
	},
	{
		name: "query",
		write: func(t *testing.T, directory string) []string {
			t.Helper()
			input := filepath.Join(directory, "user.sql")
			require.NoError(t, os.WriteFile(input, []byte(`SELECT id FROM users WHERE id = {{bind "id"}}`), 0o600))
			return []string{"query", "-input", input, "-function", "UserByID", "-dialect", "postgresql", "-package", "generated"}
		},
	},
}

// TestRunRejectsDirectoryOutput pins the fix for a regression that symlink
// resolution introduced into resolveOutputPath: an -output path naming an
// existing directory must be rejected instead of being written into.
// withResolvedParent used to rebuild the destination as
// filepath.Join(filepath.Dir(path), filepath.Base(path)); for a path
// ending in a separator, filepath.Dir strips the trailing slash and
// reports the directory itself, so that join turned "outdir/" into a new
// child file "outdir/outdir" inside it and the run reported success. A
// path without a trailing slash already failed, but only because
// os.Rename reported "file exists" on top of a directory, not because
// resolveOutputPath diagnosed it directly. Both forms must now fail with
// the same clear diagnosis, and neither must leave a file behind.
func TestRunRejectsDirectoryOutput(t *testing.T) {
	for _, fixture := range outputCommandFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			for _, tc := range []struct {
				name          string
				trailingSlash bool
			}{
				{name: "trailing slash", trailingSlash: true},
				{name: "no trailing slash", trailingSlash: false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					directory, err := os.MkdirTemp(".", ".tmp-output-directory-*")
					require.NoError(t, err)
					t.Cleanup(func() {
						require.NoError(t, os.RemoveAll(directory))
					})
					outputDirectory := filepath.Join(directory, "outdir")
					require.NoError(t, os.Mkdir(outputDirectory, 0o700))
					output := outputDirectory
					if tc.trailingSlash {
						output += string(filepath.Separator)
					}
					args := append(fixture.write(t, directory), "-output", output)

					err = run(args)

					require.ErrorContains(t, err, "is a directory")
					entries, err := os.ReadDir(outputDirectory)
					require.NoError(t, err)
					require.Empty(t, entries, "no file must be created inside the rejected directory")
				})
			}
		})
	}
}

// TestRunAcceptsSafeOutputPaths is coverage, not discrimination: both cases
// here already passed before the directory-rejection fix in
// resolveOutputPath, so neither fails against the pre-fix code. They guard
// the "-output under a missing directory reports an error" and "an
// ordinary new -output path succeeds" behavior against a future regression
// introduced by the new directory check itself, across both subcommands.
func TestRunAcceptsSafeOutputPaths(t *testing.T) {
	for _, fixture := range outputCommandFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Run("missing output directory reports an error", func(t *testing.T) {
				directory, err := os.MkdirTemp(".", ".tmp-output-directory-*")
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, os.RemoveAll(directory))
				})
				output := filepath.Join(directory, "missing", "out.go")
				args := append(fixture.write(t, directory), "-output", output)

				err = run(args)

				require.Error(t, err)
			})

			t.Run("ordinary new output path succeeds", func(t *testing.T) {
				directory, err := os.MkdirTemp(".", ".tmp-output-directory-*")
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, os.RemoveAll(directory))
				})
				output := filepath.Join(directory, "out.go")
				args := append(fixture.write(t, directory), "-output", output)

				require.NoError(t, run(args))
				_, err = os.Stat(output)
				require.NoError(t, err)
			})
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
	mock.ExpectBegin()
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
	mock.ExpectCommit()
	mock.ExpectClose()

	output := filepath.Join(directory, "schema.go")
	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", output})
	require.NoError(t, err)
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Users() UsersTable {")
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
				output := filepath.Join(directory, "schema.go")
				data := []byte(`[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`)
				require.NoError(t, os.WriteFile(input, data, 0o600))

				err = run(append(source.args(input, output), "-timeout", timeout.value))
				require.ErrorContains(t, err, "schema -timeout must be positive")
				_, err = os.Stat(output)
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

	output := filepath.Join(directory, "schema.go")
	start := time.Now()
	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-timeout", "20ms", "-package", "generated", "-output", output})
	elapsed := time.Since(start)
	// Keep the full prefix, not just the cancellation tail, so this cannot be
	// satisfied by a cancellation raised at some other query.
	require.ErrorContains(t, err, "read PostgreSQL server version: canceling query due to user request")
	// Bound against the delay constant itself, not a smaller round number:
	// observed elapsed is ~21ms against a 500ms delay, a ~24x margin. A
	// tighter bound (e.g. 50ms) would add no discrimination and could flake
	// on a loaded machine.
	require.Less(t, elapsed, inspectionDelay, "inspection must be cut short by -timeout, took %s", elapsed)
	_, err = os.Stat(output)
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

	output := filepath.Join(directory, "schema.go")
	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", output})
	require.ErrorContains(t, err, "begin PostgreSQL inspection transaction")
	require.ErrorContains(t, err, "connection refused")
	_, err = os.Stat(output)
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

	output := filepath.Join(directory, "schema.go")
	err = run([]string{"schema", "-dsn", "postgres://example", "-table", "users", "-package", "generated", "-output", output})
	require.Error(t, err)

	select {
	case opts := <-recorder.recorded:
		require.Equal(t, sql.LevelRepeatableRead, sql.IsolationLevel(opts.Isolation))
		require.True(t, opts.ReadOnly)
	default:
		t.Fatal("BeginTx was not called")
	}
	_, err = os.Stat(output)
	require.ErrorIs(t, err, os.ErrNotExist)
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

	const schemaContent = `[{"Name":"users","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}]`
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
			generated := filepath.Join(directory, "generated.go")
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
