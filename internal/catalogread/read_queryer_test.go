package catalogread_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

const sqliteCatalogObjects = `SELECT name, type, sql FROM "main".sqlite_master WHERE type IN \('table', 'view'\)`

func TestReadQueryerEntryPointParity(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		want := []schema.TableDef{
			{Schema: "main", Name: "alpha", Kind: schema.ObjectTable, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Nullable: false}, {Name: "payload", Type: schema.TextType{}, Nullable: false}}, PrimaryKey: []string{"id"}},
			{Schema: "main", Name: "zeta", Kind: schema.ObjectTable, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Nullable: false}, {Name: "payload", Type: schema.TextType{}, Nullable: false}}, PrimaryKey: []string{"id"}},
		}
		for _, entry := range []struct {
			name string
			read func(*testing.T, *sql.DB, engineprofile.Profile) (catalogread.Result, error)
		}{
			{name: "Read", read: func(t *testing.T, db *sql.DB, profile engineprofile.Profile) (catalogread.Result, error) {
				return catalogread.Read(t.Context(), db, profile, catalogread.Scope{})
			}},
			{name: "ReadTx", read: func(t *testing.T, db *sql.DB, profile engineprofile.Profile) (catalogread.Result, error) {
				conn := openSQLiteConn(t, db)
				tx, err := conn.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				result, readErr := catalogread.ReadTx(t.Context(), tx, profile, catalogread.Scope{})
				var probe int
				require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
				require.NoError(t, tx.Rollback())
				return result, readErr
			}},
			{name: "ReadConn", read: func(t *testing.T, db *sql.DB, profile engineprofile.Profile) (catalogread.Result, error) {
				conn := openSQLiteConn(t, db)
				result, readErr := catalogread.ReadConn(t.Context(), conn, profile, catalogread.Scope{})
				var probe int
				require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
				return result, readErr
			}},
		} {
			t.Run(entry.name, func(t *testing.T) {
				db := openFileSQLite(t)
				_, err := db.ExecContext(t.Context(), "CREATE TABLE zeta (id INTEGER PRIMARY KEY, payload TEXT NOT NULL); CREATE TABLE alpha (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)")
				require.NoError(t, err)
				got, err := entry.read(t, db, sqliteProfile(t))
				require.NoError(t, err)
				require.Equal(t, want, got.Tables)
				require.Equal(t, sqliteProfile(t), got.Observed)
				require.Empty(t, got.Unresolved)
			})
		}
	})

	t.Run("exact missing include", func(t *testing.T) {
		var wantError string
		for _, entry := range []string{"Read", "ReadTx", "ReadConn"} {
			t.Run(entry, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() {
					mock.ExpectClose()
					require.NoError(t, db.Close())
					require.NoError(t, mock.ExpectationsWereMet())
				})
				var got catalogread.Result
				var readErr error
				var tx *sql.Tx
				var conn *sql.Conn
				scope := catalogread.Scope{Include: []schema.ObjectName{{Name: "missing"}}}
				switch entry {
				case "Read":
					mock.ExpectBegin()
					mock.ExpectQuery("PRAGMA table_list").WillReturnError(errors.New("legacy fixture"))
					mock.ExpectQuery("PRAGMA database_list").WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
					mock.ExpectQuery(sqliteCatalogObjects).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "sql"}))
					mock.ExpectRollback()
					got, readErr = catalogread.Read(t.Context(), db, sqliteProfile(t), scope)
				case "ReadTx":
					mock.ExpectBegin()
					var beginErr error
					tx, beginErr = db.BeginTx(t.Context(), nil)
					require.NoError(t, beginErr)
					mock.ExpectQuery("PRAGMA table_list").WillReturnError(errors.New("legacy fixture"))
					mock.ExpectQuery("PRAGMA database_list").WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
					mock.ExpectQuery(sqliteCatalogObjects).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "sql"}))
					got, readErr = catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), scope)
					mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
					var probe int
					require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
					mock.ExpectRollback()
					require.NoError(t, tx.Rollback())
				case "ReadConn":
					var connErr error
					conn, connErr = db.Conn(t.Context())
					require.NoError(t, connErr)
					t.Cleanup(func() { require.NoError(t, conn.Close()) })
					mock.ExpectQuery("PRAGMA table_list").WillReturnError(errors.New("legacy fixture"))
					mock.ExpectQuery("PRAGMA database_list").WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
					mock.ExpectQuery(sqliteCatalogObjects).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "sql"}))
					got, readErr = catalogread.ReadConn(t.Context(), conn, sqliteProfile(t), scope)
					mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
					var probe int
					require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
				}
				require.ErrorIs(t, readErr, catalogread.ErrUnresolvedFact)
				require.ErrorIs(t, readErr, inspect.ErrTableNotFound)
				require.Empty(t, got)
				if wantError == "" {
					wantError = readErr.Error()
				} else {
					require.EqualError(t, readErr, wantError)
				}
			})
		}
	})

	t.Run("classified sweep facts", func(t *testing.T) {
		var wantResult catalogread.Result
		for _, entry := range []string{"Read", "ReadTx", "ReadConn"} {
			t.Run(entry, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() {
					mock.ExpectClose()
					require.NoError(t, db.Close())
					require.NoError(t, mock.ExpectationsWereMet())
				})
				profile := transactionAcceptanceProfile(t)
				rows := sqlmock.NewRows([]string{"relname", "kind"}).AddRow("z_missing", "table").AddRow("a_partial", "table")
				var got catalogread.Result
				var readErr error
				var tx *sql.Tx
				var conn *sql.Conn
				switch entry {
				case "Read":
					mock.ExpectBegin()
				case "ReadTx":
					mock.ExpectBegin()
					var beginErr error
					tx, beginErr = db.BeginTx(t.Context(), nil)
					require.NoError(t, beginErr)
					defer func() {
						mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
						var probe int
						require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
						mock.ExpectRollback()
						require.NoError(t, tx.Rollback())
					}()
				case "ReadConn":
					var connErr error
					conn, connErr = db.Conn(t.Context())
					require.NoError(t, connErr)
					defer func() {
						mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
						var probe int
						require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
						require.NoError(t, conn.Close())
					}()
				}
				mock.ExpectQuery(enumerateObjects).WillReturnRows(rows)
				mock.ExpectQuery(inspectVersion).WillReturnError(fmt.Errorf("missing fixture: %w", inspect.ErrTableNotFound))
				mock.ExpectQuery(inspectVersion).WillReturnError(fmt.Errorf("partial fixture: %w", inspect.ErrIncompleteMetadata))
				if entry == "Read" {
					mock.ExpectCommit()
				}
				switch entry {
				case "Read":
					got, readErr = catalogread.Read(t.Context(), db, profile, catalogread.Scope{})
				case "ReadTx":
					got, readErr = catalogread.ReadTx(t.Context(), tx, profile, catalogread.Scope{})
				case "ReadConn":
					got, readErr = catalogread.ReadConn(t.Context(), conn, profile, catalogread.Scope{})
				}
				require.NoError(t, readErr)
				require.Equal(t, []catalogread.UnresolvedFact{
					{Object: schema.ObjectName{Name: "a_partial"}, Path: "columns", Code: "columns_incomplete", Detail: "inspect: read PostgreSQL server version: partial fixture: inspect: incomplete table metadata"},
					{Object: schema.ObjectName{Name: "z_missing"}, Path: "$", Code: "object_missing", Detail: "inspect: read PostgreSQL server version: missing fixture: inspect: table not found"},
				}, got.Unresolved)
				if entry == "Read" {
					wantResult = got
				} else {
					require.Equal(t, wantResult, got)
				}
			})
		}
	})

	t.Run("fatal enumeration failure", func(t *testing.T) {
		var wantError string
		for _, entry := range []string{"Read", "ReadTx", "ReadConn"} {
			t.Run(entry, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() {
					mock.ExpectClose()
					require.NoError(t, db.Close())
					require.NoError(t, mock.ExpectationsWereMet())
				})
				var got catalogread.Result
				var readErr error
				var tx *sql.Tx
				var conn *sql.Conn
				switch entry {
				case "Read":
					mock.ExpectBegin()
				case "ReadTx":
					mock.ExpectBegin()
					var beginErr error
					tx, beginErr = db.BeginTx(t.Context(), nil)
					require.NoError(t, beginErr)
					defer func() {
						mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
						var probe int
						require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
						mock.ExpectRollback()
						require.NoError(t, tx.Rollback())
					}()
				case "ReadConn":
					var connErr error
					conn, connErr = db.Conn(t.Context())
					require.NoError(t, connErr)
					defer func() {
						mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
						var probe int
						require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
						require.NoError(t, conn.Close())
					}()
				}
				mock.ExpectQuery(enumerateObjects).WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("broken"))
				if entry == "Read" {
					mock.ExpectRollback()
				}
				switch entry {
				case "Read":
					got, readErr = catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
				case "ReadTx":
					got, readErr = catalogread.ReadTx(t.Context(), tx, transactionAcceptanceProfile(t), catalogread.Scope{})
				case "ReadConn":
					got, readErr = catalogread.ReadConn(t.Context(), conn, transactionAcceptanceProfile(t), catalogread.Scope{})
				}
				require.Error(t, readErr)
				require.NotErrorIs(t, readErr, catalogread.ErrUnresolvedFact)
				require.Empty(t, got)
				if wantError == "" {
					wantError = readErr.Error()
				} else {
					require.EqualError(t, readErr, wantError)
				}
			})
		}
	})
}

func TestReadQueryerEntryPointsValidateDuplicateScopeBeforeInspector(t *testing.T) {
	scope := catalogread.Scope{Include: []schema.ObjectName{{Name: "same"}}, Exclude: []schema.ObjectName{{Name: "same"}}}
	for _, entry := range []string{"Read", "ReadTx", "ReadConn"} {
		t.Run(entry, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			var readErr error
			switch entry {
			case "Read":
				mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
				_, readErr = catalogread.Read(t.Context(), db, sqliteProfile(t), scope)
				var probe int
				require.NoError(t, db.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
			case "ReadTx":
				mock.ExpectBegin()
				tx, beginErr := db.BeginTx(t.Context(), nil)
				require.NoError(t, beginErr)
				_, readErr = catalogread.ReadTx(t.Context(), tx, sqliteProfile(t), scope)
				mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
				var probe int
				require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
				mock.ExpectRollback()
				require.NoError(t, tx.Rollback())
			case "ReadConn":
				conn, connErr := db.Conn(t.Context())
				require.NoError(t, connErr)
				t.Cleanup(func() { require.NoError(t, conn.Close()) })
				mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1))
				_, readErr = catalogread.ReadConn(t.Context(), conn, sqliteProfile(t), scope)
				var probe int
				require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT 1").Scan(&probe))
			}
			require.Error(t, readErr)
			require.Contains(t, readErr.Error(), "duplicate object")
		})
	}
}

func TestReadQueryerEntryPointsValidateBeforeCallerState(t *testing.T) {
	profile := sqliteProfile(t)
	_, err := catalogread.ReadTx(t.Context(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog transaction must not be nil")
	_, err = catalogread.ReadConn(t.Context(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog connection must not be nil")
	custom, err := engineprofile.New("custom:test", engineprofile.Custom, "test", engineprofile.Version{}, engineprofile.Capabilities{}, engineprofile.Limits{MaxBindParameters: 1})
	require.NoError(t, err)
	_, err = catalogread.ReadTx(t.Context(), nil, custom, catalogread.Scope{})
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
	_, err = catalogread.ReadConn(t.Context(), nil, custom, catalogread.Scope{})
	require.ErrorIs(t, err, engineprofile.ErrUnsupportedFeature)
}

func TestReadQueryerNilCallerAndInspectorErrors(t *testing.T) {
	profile := sqliteProfile(t)
	_, err := catalogread.ReadTx(t.Context(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog transaction must not be nil")
	_, err = catalogread.ReadConn(t.Context(), nil, profile, catalogread.Scope{})
	require.EqualError(t, err, "catalog connection must not be nil")
	var invalid engineprofile.Profile
	_, err = catalogread.ReadTx(t.Context(), nil, invalid, catalogread.Scope{})
	require.Error(t, err)
}
