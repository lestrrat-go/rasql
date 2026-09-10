//go:build unix

package schemasource_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/schemasource"
)

// TestReadCreatesAndDropsScratchDatabase is design section 10 item 3: a scratch Read creates a
// database and drops it again, on both a normal run and one that fails partway through, on both
// live engines. The "midway failure" case points MigrationsDir at a directory that does not
// exist, so migrationdir.Load fails only after the scratch database has already been created and
// its cleanup registered.
func TestReadCreatesAndDropsScratchDatabase(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		cfg := dbtest.PostgreSQLConfig(t)
		runReadCreatesAndDropsScratchDatabase(t, "postgresql", cfg.ConnString(), func(t *testing.T) []string {
			return listPostgreSQLScratchDatabases(t, cfg)
		})
	})
	t.Run("mysql", func(t *testing.T) {
		cfg := dbtest.MySQLConfig(t)
		runReadCreatesAndDropsScratchDatabase(t, "mysql", cfg.FormatDSN(), func(t *testing.T) []string {
			return listMySQLScratchDatabases(t, cfg)
		})
	})
}

func runReadCreatesAndDropsScratchDatabase(t *testing.T, dialect, bootstrapDSN string, listScratch func(*testing.T) []string) {
	t.Helper()
	for _, tc := range []struct {
		name          string
		migrationsDir string
		wantErr       bool
	}{
		{name: "success", wantErr: false},
		{name: "midway_failure", migrationsDir: "does-not-exist", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if before := listScratch(t); len(before) != 0 {
				t.Fatalf("scratch databases already present before Read: %v", before)
			}
			req := schemasource.ReadRequest{
				ModuleRoot:    t.TempDir(),
				Dialect:       dialect,
				DSN:           bootstrapDSN,
				Scratch:       true,
				TempRoot:      t.TempDir(),
				MigrationsDir: tc.migrationsDir,
			}
			_, err := schemasource.Read(t.Context(), req, schemasource.DefaultDependencies())
			if tc.wantErr && err == nil {
				t.Fatal("Read returned nil error for a nonexistent migrations directory")
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
			if after := listScratch(t); len(after) != 0 {
				t.Fatalf("scratch databases remain after Read: %v", after)
			}
		})
	}
}

// TestReadRefusesNonScratchWithoutHistoryTableCreatesNothing pins the amendment to T9 that
// replaces its original "document the exception" resolution: pointing Read at a non-scratch
// database with no migration history table creates nothing at all - not even the history table -
// and returns the refusal naming "rasql migrate apply -dir <dir>", before migrate.Runner.Status
// is ever called. The table list is taken before and after and required to be identical, on both
// PostgreSQL and MySQL.
func TestReadRefusesNonScratchWithoutHistoryTableCreatesNothing(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		cfg := dbtest.PostgreSQLConfig(t)
		db := stdlib.OpenDB(*cfg)
		defer func() { _ = db.Close() }()
		runReadRefusesWithoutHistoryTable(t, "postgresql", cfg.ConnString(), db, listPostgreSQLTables)
	})
	t.Run("mysql", func(t *testing.T) {
		cfg := dbtest.MySQLConfig(t)
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		runReadRefusesWithoutHistoryTable(t, "mysql", cfg.FormatDSN(), db, listMySQLTables)
	})
}

func runReadRefusesWithoutHistoryTable(t *testing.T, dialect, dsn string, db *sql.DB, listTables func(*testing.T, *sql.DB) []string) {
	t.Helper()
	root := t.TempDir()
	migrationDir := filepath.Join(root, "migrations", "001_initial")
	if err := os.MkdirAll(migrationDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migrationDir, "1.up.sql"), []byte("CREATE TABLE read_refuses_t (id INTEGER)"), 0600); err != nil {
		t.Fatal(err)
	}
	before := listTables(t, db)
	req := schemasource.ReadRequest{ModuleRoot: root, Dialect: dialect, DSN: dsn, MigrationsDir: "migrations"}
	_, err := schemasource.Read(t.Context(), req, schemasource.DefaultDependencies())
	if err == nil {
		t.Fatal("Read returned nil error for a database with no migration history table")
	}
	if !strings.Contains(err.Error(), "rasql migrate apply -dir migrations") {
		t.Fatalf("error = %q, want it to name the fix", err.Error())
	}
	after := listTables(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("tables changed: before=%v after=%v", before, after)
	}
}

func listPostgreSQLScratchDatabases(t *testing.T, cfg *pgx.ConnConfig) []string {
	t.Helper()
	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()
	return queryStrings(t, db, "SELECT datname FROM pg_database WHERE datname LIKE 'rasql_schema_%'")
}

func listMySQLScratchDatabases(t *testing.T, cfg *mysql.Config) []string {
	t.Helper()
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	return queryStrings(t, db, "SHOW DATABASES LIKE 'rasql\\_schema\\_%'")
}

func listPostgreSQLTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name")
}

func listMySQLTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, "SHOW TABLES")
}

func queryStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}
