package examples_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	clirasql "github.com/lestrrat-go/rasql/cli/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
)

// TestTaskboardOfflineCheckAndDrift is the DSN-free gate CI runs: rasql codegen check passes on
// the checked-in sample without touching any database, and a stale migration, query, setting, or
// generated file each name the group they belong to and leave the working tree unchanged.
func TestTaskboardOfflineCheckAndDrift(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	cli := buildTaskboardCLI(t, repoRoot)
	source := filepath.Join(repoRoot, "sample", "taskboard")
	committed := snapshotTaskboardGenerated(t, source)

	clean := copyTaskboardModuleForOffline(t, source)
	output, err := runTaskboardCLI(t, cli, clean, "codegen", "check")
	require.NoError(t, err, "offline check on the checked-in sample: %s", output)
	require.Equal(t, committed, snapshotTaskboardGenerated(t, clean), "offline check wrote something")

	for _, test := range []struct {
		name   string
		needle string
		change func(*testing.T, string)
	}{
		{
			name:   "migrations",
			needle: "migrations",
			change: func(t *testing.T, module string) {
				path := filepath.Join(module, "db", "migrations", "001_initial", "001_create_members.up.sql")
				appendTaskboardBytes(t, path, []byte("\n-- offline migration drift\n"))
			},
		},
		{
			name:   "queries",
			needle: "queries",
			change: func(t *testing.T, module string) {
				path := filepath.Join(module, "queries", "overdue_count.sql")
				appendTaskboardBytes(t, path, []byte("\n-- offline query drift\n"))
			},
		},
		{
			name:   "settings",
			needle: "settings",
			change: func(t *testing.T, module string) {
				path := filepath.Join(module, "rasql.json")
				contents, err := os.ReadFile(path)
				require.NoError(t, err)
				var config map[string]any
				require.NoError(t, json.Unmarshal(contents, &config))
				config["mappings"] = map[string]any{
					"scalars": []any{map[string]any{
						"name": "offline.Text", "match": map[string]any{"logical_kind": "text"},
						"go_type": "string", "codec": "text",
					}},
				}
				updated, err := json.MarshalIndent(config, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(updated, '\n'), 0o644))
			},
		},
		{
			name:   "outputs",
			needle: "outputs",
			change: func(t *testing.T, module string) {
				path := filepath.Join(module, "internal", "store", "members_gen.go")
				appendTaskboardBytes(t, path, []byte("\n// offline output drift\n"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := copyTaskboardModuleForOffline(t, source)
			test.change(t, module)
			before := snapshotTaskboardGenerated(t, module)
			output, err := runTaskboardCLI(t, cli, module, "codegen", "check")
			require.Error(t, err, "offline check accepted stale %s input", test.name)
			require.Contains(t, strings.ToLower(output), test.needle,
				"offline check did not name stale %s input: %s", test.name, output)
			require.Equal(t, before, snapshotTaskboardGenerated(t, module),
				"stale %s check changed the working tree", test.name)
		})
	}
}

// TestTaskboardLiveCheckMatchesGeneratedStore is the real producer's output through the real
// consumer: a fresh PostgreSQL database gets db/migrations applied to it, and rasql codegen check
// -dsn is required to accept the checked-in store against that database, exactly as CI's
// integration job does. This is a claim about what a live server reports, so it is guarded by
// internal/dbtest per CLAUDE.md rather than resting on the offline test above.
//
// rasql.json declares a typed query, so check -dsn opens a second connection of its own through
// querydescribe.NewPostgreSQL to describe it, using pgx.Connect on the literal DSN string rather
// than the *sql.DB check's other steps share -- stdlib.RegisterConnConfig's key is invisible to
// that connector, so this builds a real postgres:// URL from the fields dbtest resolved instead of
// trusting pgx.ConnConfig.ConnString(), which the campaign's decision 13 warns still names the
// shared bootstrap database after PostgreSQLConfig repoints .Database at a fresh one.
func TestTaskboardLiveCheckMatchesGeneratedStore(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	source := filepath.Join(repoRoot, "sample", "taskboard")
	module := copyTaskboardModuleForOffline(t, source)

	db := dbtest.PostgreSQLDB(t)
	migrations, err := migrationdir.Load(filepath.Join(module, "db", "migrations"))
	require.NoError(t, err)
	runner, err := migrate.New(db, dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = runner.Apply(context.Background(), migrate.AllPending(), migrations...)
	require.NoError(t, err)

	dsn := postgresDSNFromConfig(dbtest.PostgreSQLConfig(t))

	var output, diagnostics bytes.Buffer
	configPath := filepath.Join(module, "rasql.json")
	err = clirasql.Run([]string{"codegen", "check", "-config", configPath, "-dsn", dsn}, &output, &diagnostics)
	require.NoError(t, err, "live check against a freshly migrated database: %s / %s", output.String(), diagnostics.String())
}

// postgresDSNFromConfig builds a postgres:// URL directly from a parsed *pgx.ConnConfig's fields.
// ConnConfig.ConnString() is not safe to use here: it returns the string pgx originally parsed,
// which for dbtest's per-test database still names the shared bootstrap database it was derived
// from, not the fresh one .Database was repointed at.
func postgresDSNFromConfig(cfg *pgx.ConnConfig) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:     "/" + cfg.Database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func buildTaskboardCLI(t *testing.T, repoRoot string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rasql")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/rasql")
	cmd.Dir = repoRoot
	cmd.Env = withoutTaskboardDSNs(os.Environ())
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "build current rasql CLI: %s", output)
	return path
}

func runTaskboardCLI(t *testing.T, cli, module string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(cli, args...)
	cmd.Dir = module
	cmd.Env = withoutTaskboardDSNs(os.Environ())
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func copyTaskboardModuleForOffline(t *testing.T, source string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "taskboard")
	require.NoError(t, copyTaskboardTree(source, destination))
	require.NoError(t, scratchmod.Repoint(filepath.Join(destination, "go.mod"), "github.com/lestrrat-go/rasql", "/unused/local/replace"))
	return destination
}

func copyTaskboardTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o644)
	})
}

// snapshotTaskboardGenerated captures rasql.sum and every generated Go file, so a test can require
// that a refused check left the working tree exactly as it found it.
func snapshotTaskboardGenerated(t *testing.T, module string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	output := filepath.Join(module, "internal", "store")
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), "_gen.go") && !strings.HasSuffix(entry.Name(), "_gen_test.go") && entry.Name() != "rasql.sum") {
			continue
		}
		relative := filepath.Join("internal", "store", entry.Name())
		result[filepath.ToSlash(relative)] = mustTaskboardFile(t, filepath.Join(module, relative))
	}
	return result
}

func mustTaskboardFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}

func appendTaskboardBytes(t *testing.T, path string, suffix []byte) {
	t.Helper()
	contents := mustTaskboardFile(t, path)
	require.NoError(t, os.WriteFile(path, append(contents, suffix...), 0o644))
}

func withoutTaskboardDSNs(environment []string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, value := range environment {
		if strings.HasPrefix(value, "TASKBOARD_DSN=") || strings.HasPrefix(value, "TASKBOARD_TEST_DSN=") {
			continue
		}
		result = append(result, value)
	}
	return result
}
