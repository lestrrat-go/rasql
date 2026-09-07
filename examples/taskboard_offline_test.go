package examples_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskboardOfflineGenerationAndDrift(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	cli := buildTaskboardCLI(t, repoRoot)
	source := filepath.Join(repoRoot, "sample", "taskboard")
	committed := snapshotTaskboardGenerated(t, source)

	clean := copyTaskboardModuleForOffline(t, source)
	firstOutput, err := runTaskboardCLI(t, cli, clean, "generate")
	require.NoError(t, err, "first offline generate: %s", firstOutput)
	first := snapshotTaskboardGenerated(t, clean)
	require.Equal(t, committed, first, "offline generate changed checked-in lock or generated files")

	secondOutput, err := runTaskboardCLI(t, cli, clean, "generate")
	require.NoError(t, err, "second offline generate: %s", secondOutput)
	require.Equal(t, first, snapshotTaskboardGenerated(t, clean), "repeated offline generate changed bytes")

	for _, test := range []struct {
		name   string
		needle string
		change func(*testing.T, string)
	}{
		{
			name:   "migration",
			needle: "source",
			change: func(t *testing.T, module string) {
				path := filepath.Join(module, "db", "migrations", "001_initial", "001_create_members.up.sql")
				appendTaskboardBytes(t, path, []byte("\n-- offline migration drift\n"))
			},
		},
		{
			name:   "query",
			needle: "queries",
			change: func(t *testing.T, module string) {
				path := filepath.Join(module, "queries", "overdue_count.sql")
				appendTaskboardBytes(t, path, []byte("\n-- offline query drift\n"))
			},
		},
		{
			name:   "config",
			needle: "mappings",
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
	} {
		t.Run(test.name, func(t *testing.T) {
			module := copyTaskboardModuleForOffline(t, source)
			before := snapshotTaskboardGenerated(t, module)
			test.change(t, module)
			output, err := runTaskboardCLI(t, cli, module, "check")
			require.Error(t, err, "offline check accepted stale %s input", test.name)
			require.Contains(t, strings.ToLower(output), test.needle,
				"offline check did not name stale %s input: %s", test.name, output)
			require.Equal(t, before, snapshotTaskboardGenerated(t, module),
				"stale %s check changed lock or generated files", test.name)
		})
	}
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
	goMod := filepath.Join(destination, "go.mod")
	contents, err := os.ReadFile(goMod)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(goMod, bytes.Replace(contents,
		[]byte("replace github.com/lestrrat-go/rasql => ../.."),
		[]byte("replace github.com/lestrrat-go/rasql => /unused/local/replace"), 1), 0o644))
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

func snapshotTaskboardGenerated(t *testing.T, module string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	result["rasql.lock.json"] = mustTaskboardFile(t, filepath.Join(module, "rasql.lock.json"))
	output := filepath.Join(module, "internal", "store")
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), "_gen.go") && !strings.HasSuffix(entry.Name(), "_gen_test.go")) {
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
	result := make([]string, 0, len(environment)+3)
	for _, value := range environment {
		if strings.HasPrefix(value, "TASKBOARD_SCHEMA_DSN=") || strings.HasPrefix(value, "TASKBOARD_DSN=") || strings.HasPrefix(value, "TASKBOARD_TEST_DSN=") {
			continue
		}
		result = append(result, value)
	}
	return result
}
