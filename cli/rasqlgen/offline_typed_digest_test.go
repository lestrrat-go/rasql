package rasqlgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/stretchr/testify/require"
)

func TestOfflineDigestRetainsKnownQueryEvidence(t *testing.T) {
	root := t.TempDir()
	queryPath := filepath.Join(root, "queries", "report.sql")
	querySource := []byte("SELECT id FROM users WHERE id = {{bind \"on\"}}\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(queryPath), 0o700))
	require.NoError(t, os.WriteFile(queryPath, querySource, 0o600))

	parameter := compilerlock.ValueRecord{
		Name: "on", Scalar: "time", LogicalKind: "time", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown,
		Native: &compilerlock.NativeTypeRecord{Dialect: "postgresql", Name: "date", Kind: "builtin"},
	}
	result := compilerlock.ValueRecord{
		Name: "overdue", Scalar: "integer", LogicalKind: "integer", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown,
		Native:  &compilerlock.NativeTypeRecord{Dialect: "postgresql", Name: "int8", Kind: "builtin"},
		Integer: &compilerlock.IntegerTypeFactsRecord{DisplayWidth: compilerlock.OptionalIntRecord{Value: 20, Set: true}},
	}
	queryRecord := compilerlock.QueryRecord{
		ID: "report", Name: "Report", SQL: compilerlock.SourceFile{Path: "queries/report.sql", SHA256: digest(querySource)}, Operation: "select", Cardinality: "many",
		Parameters: []compilerlock.ValueRecord{parameter}, Results: []compilerlock.ValueRecord{result}, Evidence: compilerlock.EngineEvidence{Dialect: "postgresql", Profile: "postgresql-16"},
	}
	source := compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "migrations", Identity: "known-evidence"}, Engine: compilerlock.EngineRecord{Dialect: "postgresql", Profile: "postgresql-16"}}
	generation := compilerir.GoConfig{Package: "store", Output: "internal/store", Emitter: "legacy", Prune: true, Queries: []compilerir.QueryGoName{{ID: "report", Function: "Report", Result: "ReportResult", Projection: "ReportProjection", Decoder: "ReportDecoder", File: "report_gen.go"}}}
	digests, err := compilerlock.BuildDigests(compilerlock.DigestInputs{Source: source, Mappings: compilerir.MappingConfig{}, Queries: []compilerlock.QueryDigestInput{{ID: string(queryRecord.ID), SQL: queryRecord.SQL, Operation: queryRecord.Operation, Parameters: queryRecord.Parameters, Results: queryRecord.Results, Cardinality: queryRecord.Cardinality}}, Generation: generation})
	require.NoError(t, err)
	lock := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Source: source.Record, Engine: source.Engine, Queries: []compilerlock.QueryRecord{queryRecord}, Generation: compilerlock.GenerationRecord{Package: generation.Package, Output: generation.Output, Emitter: generation.Emitter, Prune: generation.Prune, Queries: []compilerlock.QueryNameRecord{{ID: "report", Function: "Report", Result: "ReportResult", Projection: "ReportProjection", Decoder: "ReportDecoder", File: "report_gen.go"}}}, Digests: digests}
	settings := config{Package: "store", Output: "internal/store", Queries: []configQuery{{ID: "report", Input: "./queries/report.sql", Engine: "postgres", Function: "Report", Output: "report_gen.go", Operation: "select", Cardinality: "many", Parameters: []compilerquery.ValueDeclaration{{Name: "on", Scalar: "time", Nullable: boolPtr(false)}}, Results: []compilerquery.ValueDeclaration{{Name: "overdue", Scalar: "integer", Nullable: boolPtr(false)}}}}}

	groups, err := offlineDigestGroups(root, settings, lock)
	require.NoError(t, err)
	require.Empty(t, groups)

	settings.Queries[0].Function = "RenamedReport"
	groups, err = offlineDigestGroups(root, settings, lock)
	require.NoError(t, err)
	require.Equal(t, []string{"generation"}, groups)
}

func TestOfflineTypedQueryPolicyChangesAreStaleWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, typedSQLFixture)
	}{
		{name: "sql bytes", change: func(t *testing.T, fixture typedSQLFixture) {
			require.NoError(t, os.WriteFile(fixture.queryPath, []byte(fixture.changedQuery), 0o600))
		}},
		{name: "input path", change: func(t *testing.T, fixture typedSQLFixture) {
			path := filepath.Join(fixture.root, "queries", "renamed.sql")
			require.NoError(t, os.WriteFile(path, workflowRead(t, fixture.queryPath), 0o600))
			updateTypedQueryConfig(t, fixture.configPath, func(query map[string]any) { query["input"] = "queries/renamed.sql" })
		}},
		{name: "engine", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryConfig(t, fixture.configPath, func(query map[string]any) { query["engine"] = "postgresql" })
		}},
		{name: "operation", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryConfig(t, fixture.configPath, func(query map[string]any) { query["operation"] = "exec" })
		}},
		{name: "cardinality", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryConfig(t, fixture.configPath, func(query map[string]any) { query["cardinality"] = "one" })
		}},
		{name: "parameter name", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryValue(t, fixture.configPath, "parameters", func(value map[string]any) { value["name"] = "changed" })
		}},
		{name: "parameter scalar", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryValue(t, fixture.configPath, "parameters", func(value map[string]any) { value["scalar"] = "text" })
		}},
		{name: "parameter nullability", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryValue(t, fixture.configPath, "parameters", func(value map[string]any) { value["nullable"] = true })
		}},
		{name: "result name", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryValue(t, fixture.configPath, "results", func(value map[string]any) { value["name"] = "changed" })
		}},
		{name: "result scalar", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryValue(t, fixture.configPath, "results", func(value map[string]any) { value["scalar"] = "text" })
		}},
		{name: "result nullability", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueryValueAt(t, fixture.configPath, "results", 3, func(value map[string]any) { value["nullable"] = false })
		}},
		{name: "query removal", change: func(t *testing.T, fixture typedSQLFixture) { updateTypedQueriesConfig(t, fixture.configPath, nil) }},
		{name: "query addition", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueriesConfig(t, fixture.configPath, func(queries []any) []any { return append(queries, cloneTypedQuery(queries[0], "extra")) })
		}},
		{name: "query duplicate", change: func(t *testing.T, fixture typedSQLFixture) {
			updateTypedQueriesConfig(t, fixture.configPath, func(queries []any) []any { return append(queries, cloneTypedQuery(queries[0], "events_since")) })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTypedSQLFixture(t)
			var output, diagnostics bytes.Buffer
			command := fixture.command(t, &output, &diagnostics)
			require.NoError(t, command.run([]string{"schema", "update", "-config", fixture.configPath}), diagnostics.String())
			beforeFiles := snapshotGeneratedFiles(t, fixture.root)
			beforeLock := workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json"))
			test.change(t, fixture)
			poison := &materializationCounters{}
			command.schemaDependencies = func() schemasource.Dependencies {
				poison.provider++
				return poison.dependencies()
			}
			output.Reset()
			diagnostics.Reset()
			err := command.run([]string{"check", "-config", fixture.configPath})
			require.ErrorIs(t, err, generate.ErrStale)
			require.Equal(t, beforeFiles, snapshotGeneratedFiles(t, fixture.root))
			require.Equal(t, beforeLock, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
			require.Equal(t, 0, poison.provider)
			require.Equal(t, 0, poison.factory)
			require.Equal(t, 0, poison.opener)
			require.Equal(t, 0, poison.process)
			require.Equal(t, 0, poison.analyzer)
		})
	}
}

func TestOfflineTypedQueryGenerationNamesAreStale(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "function", change: func(query map[string]any) { query["function"] = "RenamedEventsSince" }},
		{name: "output", change: func(query map[string]any) { query["output"] = "renamed_events_gen.go" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTypedSQLFixture(t)
			var output, diagnostics bytes.Buffer
			command := fixture.command(t, &output, &diagnostics)
			require.NoError(t, command.run([]string{"schema", "update", "-config", fixture.configPath}), diagnostics.String())
			beforeFiles := snapshotGeneratedFiles(t, fixture.root)
			beforeLock := workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json"))
			updateTypedQueryConfig(t, fixture.configPath, test.change)
			output.Reset()
			diagnostics.Reset()
			err := command.run([]string{"check", "-config", fixture.configPath})
			require.ErrorIs(t, err, generate.ErrStale)
			require.Contains(t, err.Error(), "generation")
			require.Equal(t, beforeFiles, snapshotGeneratedFiles(t, fixture.root))
			require.Equal(t, beforeLock, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
		})
	}
}

func updateTypedQueryConfig(t *testing.T, path string, change func(map[string]any)) {
	t.Helper()
	updateTypedConfig(t, path, func(config map[string]any) {
		queries := config["queries"].([]any)
		change(queries[0].(map[string]any))
	})
}

func updateTypedQueryValue(t *testing.T, path, field string, change func(map[string]any)) {
	t.Helper()
	updateTypedQueryValueAt(t, path, field, 0, change)
}

func updateTypedQueryValueAt(t *testing.T, path, field string, index int, change func(map[string]any)) {
	t.Helper()
	updateTypedQueryConfig(t, path, func(query map[string]any) {
		values := query[field].([]any)
		change(values[index].(map[string]any))
	})
}

func updateTypedQueriesConfig(t *testing.T, path string, change func([]any) []any) {
	t.Helper()
	updateTypedConfig(t, path, func(config map[string]any) {
		queries := config["queries"].([]any)
		if change == nil {
			config["queries"] = []any{}
			return
		}
		config["queries"] = change(queries)
	})
}

func updateTypedConfig(t *testing.T, path string, change func(map[string]any)) {
	t.Helper()
	var config map[string]any
	require.NoError(t, json.Unmarshal(workflowRead(t, path), &config))
	change(config)
	updated, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, updated, 0o600))
}

func cloneTypedQuery(value any, id string) any {
	query := make(map[string]any)
	for key, item := range value.(map[string]any) {
		query[key] = item
	}
	query["id"] = id
	return query
}
