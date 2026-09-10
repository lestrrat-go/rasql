package rasqlgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
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
	generation := compilerir.GoConfig{Package: "store", Output: "internal/store", Emitter: "compact", Prune: true, Queries: []compilerir.QueryGoName{{ID: "report", Function: "Report", Result: "ReportResult", Projection: "ReportProjection", Decoder: "ReportDecoder", File: "report_gen.go"}}}
	digests, err := compilerlock.BuildDigests(compilerlock.DigestInputs{Source: source, Mappings: compilerir.MappingConfig{}, Queries: []compilerlock.QueryDigestInput{{ID: string(queryRecord.ID), SQL: queryRecord.SQL, Operation: queryRecord.Operation, Parameters: queryRecord.Parameters, Results: queryRecord.Results, Cardinality: queryRecord.Cardinality}}, Generation: generation})
	require.NoError(t, err)
	lock := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Source: source.Record, Engine: source.Engine, Queries: []compilerlock.QueryRecord{queryRecord}, Generation: compilerlock.GenerationRecord{Package: generation.Package, Output: generation.Output, Emitter: generation.Emitter, Prune: generation.Prune, Queries: []compilerlock.QueryNameRecord{{ID: "report", Function: "Report", Result: "ReportResult", Projection: "ReportProjection", Decoder: "ReportDecoder", File: "report_gen.go"}}}, Digests: digests}
	settings := config{Package: "store", Output: "internal/store", Queries: []configQuery{{ID: "report", Input: "./queries/report.sql", Engine: "postgres", Function: "Report", Output: "report_gen.go", Operation: "select", Cardinality: "many", Parameters: []compilerquery.ValueDeclaration{{Name: "on", Nullable: boolPtr(false)}}, Results: []compilerquery.ValueDeclaration{{Name: "overdue", Nullable: boolPtr(false)}}}}}

	groups, err := offlineDigestGroups(root, settings, lock)
	require.NoError(t, err)
	require.Empty(t, groups)

	settings.Queries[0].Function = "RenamedReport"
	groups, err = offlineDigestGroups(root, settings, lock)
	require.NoError(t, err)
	require.Equal(t, []string{"generation"}, groups)
}

func TestOfflineKnownPostgreSQLEvidenceParity(t *testing.T) {
	fixture := newKnownPostgreSQLFixture(t)
	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	command.schemaDependencies = func() schemasource.Dependencies { return fixture.dependencies() }
	require.NoError(t, command.run([]string{"schema", "update", "-config", fixture.configPath, "-dsn", "postgresql://known-evidence"}), diagnostics.String())
	onlineFiles := snapshotGeneratedFiles(t, fixture.root)
	onlineLock := workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json"))
	for path := range onlineFiles {
		require.NoError(t, os.Remove(filepath.Join(fixture.root, path)))
	}
	poison := &materializationCounters{}
	command.schemaDependencies = func() schemasource.Dependencies {
		poison.provider++
		return poison.dependencies()
	}
	output.Reset()
	diagnostics.Reset()
	require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath}), diagnostics.String())
	require.Equal(t, onlineFiles, snapshotGeneratedFiles(t, fixture.root))
	require.Equal(t, onlineLock, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
	require.Equal(t, 0, poison.provider)
	require.Equal(t, 0, poison.factory)
	require.Equal(t, 0, poison.opener)
	require.Equal(t, 0, poison.process)
	require.Equal(t, 0, poison.analyzer)
	output.Reset()
	diagnostics.Reset()
	require.NoError(t, command.run([]string{"check", "-config", fixture.configPath}), diagnostics.String())
	require.Equal(t, onlineFiles, snapshotGeneratedFiles(t, fixture.root))
	require.Equal(t, onlineLock, workflowRead(t, filepath.Join(fixture.root, "rasql.lock.json")))
	require.Equal(t, 0, poison.provider)
	require.Equal(t, 0, poison.factory)
	require.Equal(t, 0, poison.opener)
	require.Equal(t, 0, poison.process)
	require.Equal(t, 0, poison.analyzer)
	require.NoFileExists(t, filepath.Join(fixture.root, pendingMarkerName))
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

type knownPostgreSQLFixture struct {
	root, configPath, queryPath string
	query                       compilerir.QueryAnalysis
}

func newKnownPostgreSQLFixture(t *testing.T) knownPostgreSQLFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "queries"), 0o700))
	require.NoError(t, scratchmod.Write(root, repoRoot(t), "example.test/known"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "migrations", "001.sql"), []byte("-- known evidence fixture\n"), 0o600))
	queryPath := filepath.Join(root, "queries", "report.sql")
	require.NoError(t, os.WriteFile(queryPath, []byte("SELECT id, created_at FROM reports WHERE id = {{bind \"id\"}}\n"), 0o600))
	query := compilerir.QueryAnalysis{
		ID: "report", Name: "Report", SQLPath: "queries/report.sql", Operation: "select", Cardinality: "many",
		Engine:     compilerir.EngineIdentity{Dialect: "postgresql", Profile: "postgresql-16"},
		Parameters: []compilerir.SemanticValue{{Name: "id", Scalar: "integer", Nullable: false, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown, LogicalKind: "integer", Native: &compilerir.NativeType{Dialect: "postgresql", Name: "int8", Kind: "builtin"}, Integer: &compilerir.IntegerTypeFacts{}}},
		Results:    []compilerir.SemanticValue{{Name: "id", Scalar: "integer", Nullable: false, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown, LogicalKind: "integer", Native: &compilerir.NativeType{Dialect: "postgresql", Name: "int8", Kind: "builtin"}, Integer: &compilerir.IntegerTypeFacts{}}, {Name: "created_at", Scalar: "time", Nullable: false, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown, LogicalKind: "time", Native: &compilerir.NativeType{Dialect: "postgresql", Name: "timestamp", Kind: "builtin"}}},
	}
	config := map[string]any{
		"engine":  map[string]string{"dialect": "postgresql", "profile": "postgresql-16"},
		"schema":  map[string]any{"kind": "migrations", "identity": "known-postgresql", "paths": []string{"migrations/*.sql"}},
		"package": "store", "output": "internal/store", "emitter": "compact",
		"queries": []any{map[string]any{
			"id": "report", "input": "queries/report.sql", "engine": "postgresql", "function": "Report", "output": "report_gen.go",
			"operation": "select", "cardinality": "many",
			"parameters": []any{map[string]any{"name": "id", "nullable": false}},
			"results":    []any{map[string]any{"name": "id", "nullable": false}, map[string]any{"name": "created_at", "nullable": false}},
		}},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath, configBytes, 0o600))
	return knownPostgreSQLFixture{root: root, configPath: configPath, queryPath: queryPath, query: query}
}

func (f knownPostgreSQLFixture) command(t *testing.T, output, diagnostics *bytes.Buffer) *command {
	t.Helper()
	return &command{program: "rasql", output: output, diagnostics: diagnostics, ctx: t.Context()}
}

func (f knownPostgreSQLFixture) dependencies() schemasource.Dependencies {
	return schemasource.Dependencies{
		Factory:    knownPostgreSQLFactory{},
		Profiles:   knownPostgreSQLProfiles{},
		Migrations: knownPostgreSQLMigrations{},
		Catalogs:   knownPostgreSQLCatalogs{},
		Analyzer:   knownPostgreSQLAnalyzer{root: f.root, query: f.query},
	}
}

type knownPostgreSQLFactory struct{}

func (knownPostgreSQLFactory) Create(context.Context, schemasource.FactoryRequest) (schemasource.DisposableDatabase, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return schemasource.DisposableDatabase{}, err
	}
	return schemasource.DisposableDatabase{DB: db, DSN: "postgresql://known-evidence", CloseAndDrop: func(context.Context) error { return db.Close() }}, nil
}

type knownPostgreSQLProfiles struct{}

func (knownPostgreSQLProfiles) Resolve(context.Context, *sql.DB, schemasource.EngineConfig) (engineprofile.Profile, error) {
	return engineprofile.Builtin("postgresql-16", engineprofile.Version{Known: true, Major: 16})
}

type knownPostgreSQLMigrations struct{}

func (knownPostgreSQLMigrations) Apply(context.Context, *sql.DB, engineprofile.Profile, []sourcefile.SourceFileSnapshot) error {
	return nil
}

type knownPostgreSQLCatalogs struct{}

func (knownPostgreSQLCatalogs) Read(context.Context, catalogread.DB, engineprofile.Profile, catalogread.Scope) (catalogread.Result, error) {
	return catalogread.Result{Tables: []schema.TableDef{{Name: "reports", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}}}}, nil
}

type knownPostgreSQLAnalyzer struct {
	root  string
	query compilerir.QueryAnalysis
}

func (a knownPostgreSQLAnalyzer) Analyze(context.Context, schemasource.AnalysisRequest) (schemasource.AnalysisResult, error) {
	snapshot, err := sourcefile.SnapshotSourceFile(a.root, a.query.SQLPath)
	if err != nil {
		return schemasource.AnalysisResult{}, err
	}
	query := a.query
	query.SQLSHA256 = snapshot.SHA256()
	return schemasource.AnalysisResult{Queries: []compilerir.QueryAnalysis{query}, Snapshots: []sourcefile.SourceFileSnapshot{snapshot}}, nil
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
