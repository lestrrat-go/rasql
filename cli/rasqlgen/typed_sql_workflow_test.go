package rasqlgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/stretchr/testify/require"
)

// TestTypedSQLGenerateScratchProducesTypedSource proves the typed-SQL declaration path -- a
// query naming its parameters and results explicitly, with a custom scalar mapping and codec --
// survives generate -scratch and byte-identical reruns, and that the offline check that follows
// consults no database.
func TestTypedSQLGenerateScratchProducesTypedSource(t *testing.T) {
	fixture := newTypedSQLFixture(t)
	var out, diag bytes.Buffer
	command := fixture.command(t, &out, &diag)

	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diag.String())
	before := snapshotGeneratedFiles(t, fixture.root)
	require.Equal(t, []string{
		"internal/store/events_gen.go",
		"internal/store/events_since_gen.go",
		"internal/store/schema_gen.go",
		"internal/store/schema_gen_test.go",
	}, sortedFileNames(before))
	assertTypedSQLSource(t, before["internal/store/events_since_gen.go"])

	fixture.resetCommandBuffers(&out, &diag)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diag.String())
	require.Equal(t, before, snapshotGeneratedFiles(t, fixture.root))

	fixture.resetCommandBuffers(&out, &diag)
	require.NoError(t, command.run([]string{"check", "-config", fixture.configPath}), diag.String())
	require.Equal(t, before, snapshotGeneratedFiles(t, fixture.root))
}

// TestTypedSQLPublicationRaceRefusesOnChangedQuery proves a query file edited after Read has
// already snapshotted it, but before the plan is committed, refuses the commit and leaves nothing
// on disk -- the refuse-before-publication guarantee the offline path used to prove through a
// changed lock digest.
func TestTypedSQLPublicationRaceRefusesOnChangedQuery(t *testing.T) {
	fixture := newTypedSQLFixture(t)
	var out, diag bytes.Buffer
	command := fixture.command(t, &out, &diag)
	command.beforePublication = func() {
		require.NoError(t, os.WriteFile(fixture.queryPath, []byte(fixture.changedQuery), 0o600))
	}
	err := command.run([]string{"generate", "-config", fixture.configPath, "-scratch"})
	require.Error(t, err)
	require.ErrorIs(t, err, sourcefile.ErrSourceChanged)
	// Commit's step 1 may have already created the output directory's own missing path
	// components while authorizing the run; nothing later removes them, so the guarantee is an
	// empty directory (or none at all), never a written file.
	storeDir := filepath.Join(fixture.root, "internal", "store")
	if _, statErr := os.Stat(storeDir); statErr == nil {
		entries, readErr := os.ReadDir(storeDir)
		require.NoError(t, readErr)
		require.Empty(t, entries)
	} else {
		require.ErrorIs(t, statErr, os.ErrNotExist)
	}
}

type typedSQLFixture struct {
	root, configPath, queryPath string
	query, changedQuery         string
}

func newTypedSQLFixture(t *testing.T) typedSQLFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "queries"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "money"), 0o700))
	require.NoError(t, scratchmod.Write(root, repoRoot(t), "example.test/fixture"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "money", "money.go"), []byte("package money\n\ntype Money int64\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "001_init"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001_init", "1.up.sql"), []byte("CREATE TABLE events (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL, occurred_at DATETIME NOT NULL, note TEXT NULL);\nINSERT INTO events VALUES (1, 4, '2024-01-01T00:00:00Z', NULL);\nINSERT INTO events VALUES (2, 9, '2024-01-02T00:00:00Z', 'ready');\n"), 0o600))
	query := "SELECT id, amount, occurred_at, note FROM events WHERE amount > {{bind \"amount\" events.amount}} OR amount = {{bind \"amount\" events.amount}} AND occurred_at >= {{bind \"since\" events.occurred_at}}\n"
	changedQuery := "SELECT id, amount, occurred_at, note FROM events WHERE amount >= {{bind \"amount\" events.amount}} OR amount = {{bind \"amount\" events.amount}} AND occurred_at >= {{bind \"since\" events.occurred_at}}\n"
	queryPath := filepath.Join(root, "queries", "events_since.sql")
	require.NoError(t, os.WriteFile(queryPath, []byte(query), 0o600))
	falseValue := false
	trueValue := true
	config := map[string]any{
		"dialect": "sqlite", "migrations": "migrations",
		"package": "store", "output": "internal/store", "emitter": "compact",
		"mappings": map[string]any{"scalars": []any{map[string]any{
			"name": "money", "match": map[string]string{"dialect": "sqlite", "name": "MONEY", "logical_kind": "integer"}, "go_type": "money.Money",
			"imports": []any{map[string]string{"path": "example.test/fixture/money", "alias": "money"}}, "codec": "money.codec",
		}}},
		"queries": []any{map[string]any{
			"id": "events_since", "input": "queries/events_since.sql", "engine": "sqlite", "function": "EventsSince", "output": "events_since_gen.go",
			"operation": "select", "cardinality": "many",
			"parameters": []any{
				map[string]any{"name": "amount", "scalar": "money", "nullable": falseValue},
				map[string]any{"name": "since", "scalar": "time", "nullable": falseValue},
			},
			"results": []any{
				map[string]any{"name": "id", "scalar": "integer", "nullable": falseValue},
				map[string]any{"name": "amount", "scalar": "money", "nullable": falseValue},
				map[string]any{"name": "occurred_at", "scalar": "time", "nullable": falseValue},
				map[string]any{"name": "note", "scalar": "text", "nullable": trueValue},
			},
		}},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	return typedSQLFixture{root: root, configPath: configPath, queryPath: queryPath, query: query, changedQuery: changedQuery}
}

func (f typedSQLFixture) command(t *testing.T, out, diag *bytes.Buffer) *command {
	return &command{program: "rasql", output: out, diagnostics: diag, ctx: t.Context()}
}

func (f typedSQLFixture) resetCommandBuffers(out, diag *bytes.Buffer) {
	out.Reset()
	diag.Reset()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

func snapshotGeneratedFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	dir := filepath.Join(root, "internal", "store")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	files := make(map[string][]byte)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		files[filepath.ToSlash(filepath.Join("internal/store", entry.Name()))] = data
	}
	require.NotEmpty(t, files)
	return files
}

func sortedFileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func assertTypedSQLSource(t *testing.T, source []byte) {
	t.Helper()
	text := string(source)
	require.Contains(t, text, `money "example.test/fixture/money"`)
	require.Contains(t, text, `"time"`)
	require.Contains(t, text, "func EventsSince(amount money.Money, since time.Time)")
	require.Contains(t, text, "Amount     money.Money")
	require.Contains(t, text, "OccurredAt time.Time")
	require.Contains(t, text, `SQL: "SELECT id, amount, occurred_at, note FROM events WHERE amount > ? OR amount = ? AND occurred_at >= ?`)
	require.Equal(t, 2, strings.Count(text, `{Value: amount, Codec: "money.codec"}`))
	require.Contains(t, text, `{Value: since, Codec: ""}`)
	require.NotRegexp(t, regexp.MustCompile(`\bany\b`), text)
}

func workflowRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
