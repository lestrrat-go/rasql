package rasqlgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

func TestTypedSQLSchemaUpdateOfflineParity(t *testing.T) {
	fixture := newTypedSQLFixture(t)
	var out, diag bytes.Buffer
	command := fixture.command(t, &out, &diag)

	require.NoError(t, command.run([]string{"schema", "update", "-config", fixture.configPath}), diag.String())
	onlineFiles := snapshotGeneratedFiles(t, fixture.root)
	require.Equal(t, []string{
		"internal/store/events_gen.go",
		"internal/store/events_since_gen.go",
		"internal/store/schema_gen.go",
		"internal/store/schema_gen_test.go",
	}, sortedFileNames(onlineFiles))
	onlineLock := workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json"))
	assertTypedSQLLock(t, fixture, onlineLock)
	assertTypedSQLSource(t, onlineFiles["internal/store/events_since_gen.go"])
	require.NoFileExists(t, filepath.Join(fixture.root, pendingMarkerName))

	lock, err := compilerlock.Decode(onlineLock)
	require.NoError(t, err)
	for _, query := range lock.Generation.Queries {
		require.NoError(t, os.Remove(filepath.Join(fixture.root, filepath.FromSlash(filepath.Join(lock.Generation.Output, query.File)))))
	}
	for _, object := range lock.Generation.Objects {
		require.NoError(t, os.Remove(filepath.Join(fixture.root, filepath.FromSlash(filepath.Join(lock.Generation.Output, object.File)))))
	}
	require.NoError(t, os.Remove(filepath.Join(fixture.root, filepath.FromSlash(filepath.Join(lock.Generation.Output, "schema_gen.go")))))
	require.NoError(t, os.Remove(filepath.Join(fixture.root, filepath.FromSlash(filepath.Join(lock.Generation.Output, "schema_gen_test.go")))))

	poison := &materializationCounters{}
	command.schemaDependencies = func() schemasource.Dependencies {
		poison.provider++
		return poison.dependencies()
	}
	command.beforePublication = nil
	fixture.resetCommandBuffers(&out, &diag)
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath}), diag.String())
	require.Equal(t, 0, poison.provider)
	require.Equal(t, 0, poison.factory)
	require.Equal(t, 0, poison.opener)
	require.Equal(t, 0, poison.process)
	require.Equal(t, 0, poison.analyzer)
	require.Equal(t, onlineFiles, snapshotGeneratedFiles(t, fixture.root))
	require.Equal(t, onlineLock, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
	require.NoFileExists(t, filepath.Join(fixture.root, pendingMarkerName))

	generatedBefore := snapshotGeneratedFiles(t, fixture.root)
	lockBefore := workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json"))
	fixture.resetCommandBuffers(&out, &diag)
	require.NoError(t, command.run([]string{"check", "-config", fixture.configPath}), diag.String())
	require.Equal(t, 0, poison.provider)
	require.Equal(t, 0, poison.factory)
	require.Equal(t, 0, poison.opener)
	require.Equal(t, 0, poison.process)
	require.Equal(t, 0, poison.analyzer)
	require.Equal(t, generatedBefore, snapshotGeneratedFiles(t, fixture.root))
	require.Equal(t, lockBefore, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
	require.NoFileExists(t, filepath.Join(fixture.root, pendingMarkerName))
}

func TestTypedSQLPublicationRace(t *testing.T) {
	t.Run("schema update", func(t *testing.T) {
		fixture := newTypedSQLFixture(t)
		var out, diag bytes.Buffer
		command := fixture.command(t, &out, &diag)
		command.beforePublication = func() {
			require.NoError(t, os.WriteFile(fixture.queryPath, []byte(fixture.changedQuery), 0o600))
		}
		err := command.run([]string{"schema", "update", "-config", fixture.configPath})
		require.Error(t, err)
		require.ErrorContains(t, err, "source")
		if _, statErr := os.Stat(filepath.Join(fixture.root, "internal", "store")); statErr == nil {
			entries, readErr := os.ReadDir(filepath.Join(fixture.root, "internal", "store"))
			require.NoError(t, readErr)
			require.Empty(t, entries)
		} else {
			require.ErrorIs(t, statErr, os.ErrNotExist)
		}
		require.NoFileExists(t, filepath.Join(fixture.root, "rasql.lock.json"))
		require.NoFileExists(t, filepath.Join(fixture.root, pendingMarkerName))
	})

	t.Run("offline generate", func(t *testing.T) {
		fixture := newTypedSQLFixture(t)
		var out, diag bytes.Buffer
		command := fixture.command(t, &out, &diag)
		require.NoError(t, command.run([]string{"schema", "update", "-config", fixture.configPath}), diag.String())
		beforeFiles := snapshotGeneratedFiles(t, fixture.root)
		beforeLock := workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json"))
		command.beforePublication = func() {
			require.NoError(t, os.WriteFile(fixture.queryPath, []byte(fixture.changedQuery), 0o600))
		}
		fixture.resetCommandBuffers(&out, &diag)
		err := command.run([]string{"generate", "-config", fixture.configPath})
		require.Error(t, err)
		require.ErrorContains(t, err, "source")
		require.Equal(t, beforeFiles, snapshotGeneratedFiles(t, fixture.root))
		require.Equal(t, beforeLock, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
		require.NoFileExists(t, filepath.Join(fixture.root, pendingMarkerName))
	})
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
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001.sql"), []byte("CREATE TABLE events (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL, occurred_at DATETIME NOT NULL, note TEXT NULL);\nINSERT INTO events VALUES (1, 4, '2024-01-01T00:00:00Z', NULL);\nINSERT INTO events VALUES (2, 9, '2024-01-02T00:00:00Z', 'ready');\n"), 0o600))
	query := "SELECT id, amount, occurred_at, note FROM events WHERE amount > {{bind \"amount\" events.amount}} OR amount = {{bind \"amount\" events.amount}} AND occurred_at >= {{bind \"since\" events.occurred_at}}\n"
	changedQuery := "SELECT id, amount, occurred_at, note FROM events WHERE amount >= {{bind \"amount\" events.amount}} OR amount = {{bind \"amount\" events.amount}} AND occurred_at >= {{bind \"since\" events.occurred_at}}\n"
	queryPath := filepath.Join(root, "queries", "events_since.sql")
	require.NoError(t, os.WriteFile(queryPath, []byte(query), 0o600))
	falseValue := false
	trueValue := true
	config := map[string]any{
		"engine":  map[string]string{"dialect": "sqlite", "profile": "sqlite-3.35"},
		"schema":  map[string]any{"kind": "migrations", "identity": "typed-workflow", "paths": []string{"migrations/*.sql"}},
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

func assertTypedSQLLock(t *testing.T, fixture typedSQLFixture, lockBytes []byte) {
	t.Helper()
	lock, err := compilerlock.Decode(lockBytes)
	require.NoError(t, err)
	require.Equal(t, compilerlock.FormatVersion, lock.Format)
	require.Equal(t, "migrations", lock.Source.Kind)
	require.Equal(t, "typed-workflow", lock.Source.Identity)
	require.Len(t, lock.Queries, 1)
	query := lock.Queries[0]
	require.Equal(t, compilerir.QueryID("events_since"), query.ID)
	require.Equal(t, "queries/events_since.sql", query.SQL.Path)
	digest := sha256.Sum256([]byte(fixture.query))
	require.Equal(t, hex.EncodeToString(digest[:]), query.SQL.SHA256)
	require.Equal(t, "select", query.Operation)
	require.Equal(t, "many", query.Cardinality)
	require.Equal(t, []string{"amount", "since"}, valueNames(query.Parameters))
	require.Equal(t, []string{"money", "time"}, valueScalars(query.Parameters))
	require.Equal(t, []string{"integer", "time"}, valueLogicalKinds(query.Parameters))
	require.Equal(t, []compilerir.Certainty{compilerir.CertaintyDeclared, compilerir.CertaintyDeclared}, valueTypeCertainty(query.Parameters))
	require.Equal(t, []string{"id", "amount", "occurred_at", "note"}, valueNames(query.Results))
	require.Equal(t, []string{"integer", "money", "time", "text"}, valueScalars(query.Results))
	require.Equal(t, []string{"integer", "integer", "time", "text"}, valueLogicalKinds(query.Results))
	require.Equal(t, []bool{false, false, false, true}, valueNullability(query.Results))
	require.Equal(t, []string{"EventsSince", "events_since_gen.go"}, []string{lock.Generation.Queries[0].Function, lock.Generation.Queries[0].File})
	require.Equal(t, []string{"store", "internal/store", "compact"}, []string{lock.Generation.Package, lock.Generation.Output, lock.Generation.Emitter})
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

func valueNames(values []compilerlock.ValueRecord) []string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = value.Name
	}
	return names
}

func valueScalars(values []compilerlock.ValueRecord) []string {
	scalars := make([]string, len(values))
	for i, value := range values {
		scalars[i] = value.Scalar
	}
	return scalars
}

func valueLogicalKinds(values []compilerlock.ValueRecord) []string {
	kinds := make([]string, len(values))
	for i, value := range values {
		kinds[i] = value.LogicalKind
	}
	return kinds
}

func valueTypeCertainty(values []compilerlock.ValueRecord) []compilerir.Certainty {
	certainty := make([]compilerir.Certainty, len(values))
	for i, value := range values {
		certainty[i] = value.TypeCertainty
	}
	return certainty
}

func valueNullability(values []compilerlock.ValueRecord) []bool {
	nullable := make([]bool, len(values))
	for i, value := range values {
		nullable[i] = value.Nullable
	}
	return nullable
}

type materializationCounters struct {
	provider, factory, opener, process, analyzer int
}

func (c *materializationCounters) dependencies() schemasource.Dependencies {
	return schemasource.Dependencies{Factory: countedFactory{c}, Opener: countedOpener{c}, Processes: countedProcess{c}, Analyzer: countedAnalyzer{c}}
}

type countedFactory struct{ counters *materializationCounters }

func (f countedFactory) Create(context.Context, schemasource.FactoryRequest) (schemasource.DisposableDatabase, error) {
	f.counters.factory++
	return schemasource.DisposableDatabase{}, fmt.Errorf("poison factory called")
}

type countedOpener struct{ counters *materializationCounters }

func (o countedOpener) Open(string, string) (*sql.DB, error) {
	o.counters.opener++
	return nil, fmt.Errorf("poison opener called")
}

type countedProcess struct{ counters *materializationCounters }

func (p countedProcess) Run(context.Context, schemasource.ProcessRequest) (schemasource.ProcessResult, error) {
	p.counters.process++
	return schemasource.ProcessResult{}, fmt.Errorf("poison process called")
}

type countedAnalyzer struct{ counters *materializationCounters }

func (a countedAnalyzer) Analyze(context.Context, schemasource.AnalysisRequest) (schemasource.AnalysisResult, error) {
	a.counters.analyzer++
	return schemasource.AnalysisResult{}, fmt.Errorf("poison analyzer called")
}

func workflowRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
