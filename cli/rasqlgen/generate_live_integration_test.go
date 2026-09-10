//go:build unix

package rasqlgen

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
)

// liveEngine names one live server this file proves the new codegen path against, and how to
// build a project rooted in dialect-appropriate SQL for it.
type liveEngine struct {
	dialect string
	dsn     func(t *testing.T) string
	// createLegacy is a table created directly, the way a database rasql did not build already
	// holds one: no migration ever produced it.
	createLegacy string
	// migration1, migration2 are two independent migrations' forward SQL.
	migration1, migration2 string
	alterAddColumn         string
}

func liveEngines() []liveEngine {
	return []liveEngine{
		{
			dialect: "postgresql",
			// pgx.ConnConfig.ConnString() returns the string as originally parsed, not the
			// current struct fields, so it still names dbtest's own bootstrap database after
			// PostgreSQLConfig points Database at this test's fresh one. stdlib.RegisterConnConfig
			// hands database/sql a key that looks the config's live fields up instead, which is
			// what actually reaches the fresh, isolated database this test operates on.
			dsn: func(t *testing.T) string {
				key := stdlib.RegisterConnConfig(dbtest.PostgreSQLConfig(t))
				t.Cleanup(func() { stdlib.UnregisterConnConfig(key) })
				return key
			},
			createLegacy:   "CREATE TABLE legacy_accounts (id INTEGER PRIMARY KEY, name VARCHAR(100) NOT NULL)",
			migration1:     "CREATE TABLE orders (id INTEGER PRIMARY KEY, total INTEGER NOT NULL)",
			migration2:     "CREATE TABLE line_items (id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL)",
			alterAddColumn: "ALTER TABLE orders ADD COLUMN note VARCHAR(100)",
		},
		{
			dialect:        "mysql",
			dsn:            func(t *testing.T) string { return dbtest.MySQLConfig(t).FormatDSN() },
			createLegacy:   "CREATE TABLE legacy_accounts (id INTEGER PRIMARY KEY, name VARCHAR(100) NOT NULL)",
			migration1:     "CREATE TABLE orders (id INTEGER PRIMARY KEY, total INTEGER NOT NULL)",
			migration2:     "CREATE TABLE line_items (id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL)",
			alterAddColumn: "ALTER TABLE orders ADD COLUMN note VARCHAR(100)",
		},
	}
}

func liveDialectDialect(name string) dialect.Dialect {
	switch name {
	case "postgresql":
		return dialect.PostgreSQL()
	case "mysql":
		return dialect.MySQL()
	default:
		return dialect.SQLite()
	}
}

func liveSQLOpen(t *testing.T, dialectName, dsn string) *sql.DB {
	t.Helper()
	driver := "pgx"
	if dialectName == "mysql" {
		driver = "mysql"
	}
	db, err := sql.Open(driver, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(t.Context()))
	return db
}

// writeLiveProject writes a go.mod, a migrations directory holding migration1 and, if
// includeSecond, migration2 as well, and a rasql.json naming dialectName and the migrations
// directory. It returns the project's root and its config path.
func writeLiveProject(t *testing.T, e liveEngine, includeSecond bool) (root, configPath string) {
	t.Helper()
	root = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/livedb\n\ngo 1.24\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "001_orders"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001_orders", "1.up.sql"), []byte(e.migration1+";\n"), 0o600))
	if includeSecond {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "002_line_items"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "002_line_items", "1.up.sql"), []byte(e.migration2+";\n"), 0o600))
	}
	config := map[string]any{
		"dialect": e.dialect, "migrations": "migrations",
		"package": "store", "output": "internal/store", "emitter": "compact",
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	configPath = filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	return root, configPath
}

func liveCommand(t *testing.T, output, diagnostics *bytes.Buffer) *command {
	t.Helper()
	return &command{program: "rasql", output: output, diagnostics: diagnostics, ctx: t.Context()}
}

func applyLiveMigrations(t *testing.T, db *sql.DB, e liveEngine, root string, count int) {
	t.Helper()
	migrations, err := migrationdir.Load(filepath.Join(root, "migrations"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(migrations), count)
	runner, err := migrate.New(db, liveDialectDialect(e.dialect))
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migrations[:count]...)
	require.NoError(t, err)
}

// TestGenerateFromLegacyPlusMigrationsDatabase is design section 10 item 1: a database holding a
// legacy table plus an applied migration that references nothing of it produces both tables and
// excludes the migration history table and its companions, on both live engines.
func TestGenerateFromLegacyPlusMigrationsDatabase(t *testing.T) {
	for _, e := range liveEngines() {
		t.Run(e.dialect, func(t *testing.T) {
			dsn := e.dsn(t)
			db := liveSQLOpen(t, e.dialect, dsn)
			_, err := db.ExecContext(t.Context(), e.createLegacy)
			require.NoError(t, err)

			root, configPath := writeLiveProject(t, e, false)
			applyLiveMigrations(t, db, e, root, 1)

			var output, diagnostics bytes.Buffer
			command := liveCommand(t, &output, &diagnostics)
			require.NoError(t, command.run([]string{"generate", "-config", configPath, "-dsn", dsn}), diagnostics.String())

			entries, err := os.ReadDir(filepath.Join(root, "internal", "store"))
			require.NoError(t, err)
			names := make(map[string]bool, len(entries))
			for _, entry := range entries {
				names[entry.Name()] = true
			}
			require.True(t, names["legacy_accounts_gen.go"], "names = %v", names)
			require.True(t, names["orders_gen.go"], "names = %v", names)
			require.False(t, names["rasql_schema_migrations_gen.go"])
			require.False(t, names["rasql_schema_migrations_progress_gen.go"])
			require.FileExists(t, filepath.Join(root, "internal", "store", "rasql.sum"))
		})
	}
}

// TestGenerateRefusesPendingMigration is design section 10 item 2: with migrations configured and
// one of them pending, generate -dsn names it, names rasql migrate apply, exits with an error, and
// writes nothing.
func TestGenerateRefusesPendingMigration(t *testing.T) {
	for _, e := range liveEngines() {
		t.Run(e.dialect, func(t *testing.T) {
			dsn := e.dsn(t)
			db := liveSQLOpen(t, e.dialect, dsn)
			root, configPath := writeLiveProject(t, e, true)
			applyLiveMigrations(t, db, e, root, 1)

			var output, diagnostics bytes.Buffer
			command := liveCommand(t, &output, &diagnostics)
			err := command.run([]string{"generate", "-config", configPath, "-dsn", dsn})
			require.Error(t, err)
			require.Contains(t, err.Error(), "002_line_items")
			require.Contains(t, err.Error(), "rasql migrate apply -dir migrations")
			require.NoDirExists(t, filepath.Join(root, "internal", "store"))
		})
	}
}

// TestCheckLiveFailsAfterAlterTable is design section 10 item 4: check -dsn passes right after
// generate, then fails naming "outputs" after an ALTER TABLE on the database, leaving the working
// tree untouched.
func TestCheckLiveFailsAfterAlterTable(t *testing.T) {
	for _, e := range liveEngines() {
		t.Run(e.dialect, func(t *testing.T) {
			dsn := e.dsn(t)
			db := liveSQLOpen(t, e.dialect, dsn)
			root, configPath := writeLiveProject(t, e, true)
			applyLiveMigrations(t, db, e, root, 2)

			var output, diagnostics bytes.Buffer
			command := liveCommand(t, &output, &diagnostics)
			require.NoError(t, command.run([]string{"generate", "-config", configPath, "-dsn", dsn}), diagnostics.String())

			before := make(map[string][]byte)
			storeDir := filepath.Join(root, "internal", "store")
			entries, err := os.ReadDir(storeDir)
			require.NoError(t, err)
			for _, entry := range entries {
				data, readErr := os.ReadFile(filepath.Join(storeDir, entry.Name()))
				require.NoError(t, readErr)
				before[entry.Name()] = data
			}

			output.Reset()
			diagnostics.Reset()
			require.NoError(t, command.run([]string{"check", "-config", configPath, "-dsn", dsn}), diagnostics.String())

			_, err = db.ExecContext(t.Context(), e.alterAddColumn)
			require.NoError(t, err)

			output.Reset()
			diagnostics.Reset()
			checkErr := command.run([]string{"check", "-config", configPath, "-dsn", dsn})
			require.Error(t, checkErr)
			require.Contains(t, checkErr.Error(), "outputs")

			entries, err = os.ReadDir(storeDir)
			require.NoError(t, err)
			after := make(map[string][]byte)
			for _, entry := range entries {
				data, readErr := os.ReadFile(filepath.Join(storeDir, entry.Name()))
				require.NoError(t, readErr)
				after[entry.Name()] = data
			}
			require.Equal(t, before, after)
		})
	}
}
