package rasqlgen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
)

var ErrDrift = errors.New("rasql: schema drift")

func (c command) runSchemaUpdate(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "schema update")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "bootstrap connection string")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Engine == nil || cfg.Schema == nil {
		return errors.New("schema update: config requires engine and schema")
	}
	if cfg.Package == "" || cfg.Output == "" {
		return errors.New("schema update: config requires package and output")
	}
	root, err := moduleRootForConfig(*configPath)
	if err != nil {
		return err
	}
	marker, markerErr := readPending(root)
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	request := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: cfg.Engine.Dialect, Profile: cfg.Engine.Profile}, Source: schemasource.SchemaSourceConfig{Kind: cfg.Schema.Kind, Identity: cfg.Schema.Identity, Paths: cfg.Schema.Paths, Inputs: cfg.Schema.Inputs, Command: cfg.Schema.Command, Environment: cfg.Schema.Environment}, TempRoot: filepath.Join(root, ".tmp"), Scope: catalogScope(cfg)}
	if cfg.Schema.Kind == "live" {
		request.LiveDSN = *dsn
	} else {
		request.BootstrapDSN = *dsn
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	deps := schemasource.DefaultDependencies()
	if c.schemaDependencies != nil {
		deps = c.schemaDependencies()
	}
	if len(cfg.Queries) != 0 && deps.Analyzer == nil {
		queryConfig := cfg.compilerQueries(root)
		analyzer, analyzerErr := compilerquery.NewAnalyzer(queryConfig, compilerquery.Describers{PostgreSQL: querydescribe.NewPostgreSQL(), MySQL: querydescribe.NewMySQL(nil), SQLite: querydescribe.NewSQLitePrepare(nil)})
		if analyzerErr != nil {
			return analyzerErr
		}
		deps.Analyzer = analyzer
	}
	result, err := schemasource.Materialize(ctx, request, deps)
	if err != nil {
		return fmt.Errorf("schema update: %w", err)
	}
	var idDiagnostics []compilerir.Diagnostic
	result.Catalog, idDiagnostics = compilerir.AssignObjectIDs(result.Catalog, compilerir.IdentityInput{SourceIdentity: result.Source.Record.Identity})
	if hasErrors(idDiagnostics) {
		return errors.New("schema update: object identity assignment failed")
	}
	mappings, err := cfg.mappings()
	if err != nil {
		return err
	}
	semantic, diagnostics := compilerir.BuildSemantic(result.Catalog, mappings, result.Queries)
	if hasErrors(diagnostics) {
		return fmt.Errorf("schema update: semantic analysis failed")
	}
	prune := true
	if cfg.Prune != nil {
		prune = *cfg.Prune
	}
	emitter := cfg.Emitter
	if emitter == "" {
		emitter = "compact"
	}
	generation := compilerir.GoConfig{Package: cfg.Package, Output: cfg.Output, Emitter: emitter, Prune: prune, Scalars: mappings.Scalars}
	rowNames := cfg.Tables.RowNames
	configuredNames, err := cfg.names()
	if err != nil {
		return err
	}
	for _, object := range result.Catalog.Objects {
		name := object.Name
		if row := rowNames[object.Name]; row != "" {
			name = row
		}
		if override, ok := configuredNames[schemaObjectName(object.Schema, object.Name)]; ok && override.RowType != "" {
			name = override.RowType
		}
		name = exportGoName(name)
		generation.Objects = append(generation.Objects, compilerir.ObjectGoName{ID: object.ID, Source: name, Row: name + "Row", Create: name + "Create", Patch: name + "Patch", File: object.Name + "_gen.go"})
	}
	for _, query := range result.Queries {
		configured := queryConfigFor(cfg, query.ID)
		file := configured.Output
		if file == "" {
			file = derivedQueryOutput(configured.Input)
		}
		function := configured.Function
		generation.Queries = append(generation.Queries, compilerir.QueryGoName{ID: query.ID, Function: function, Result: string(query.ID) + "Result", Projection: string(query.ID) + "Projection", Decoder: string(query.ID) + "Decoder", File: file})
	}
	goModel, diagnostics := compilerir.BuildGo(semantic, generation)
	if hasErrors(diagnostics) {
		return fmt.Errorf("schema update: Go model failed")
	}
	for i, object := range goModel.Objects {
		generation.Objects[i].Source = exportGoName(object.SourceName)
		generation.Objects[i].Row = exportGoName(object.Row.Name)
		if object.Create != nil {
			generation.Objects[i].Create = exportGoName(object.Create.Name)
		}
		if object.Patch != nil {
			generation.Objects[i].Patch = exportGoName(object.Patch.Name)
		}
	}
	input, err := generate.NewEmitterInput(result.Catalog, semantic, goModel, generation, mappings)
	if err != nil {
		return err
	}
	store, err := renderEmitter(input)
	if err != nil {
		return err
	}
	store.Root = root
	store.Dir = cfg.Output
	goQueries := make(map[compilerir.QueryID]compilerir.GoQuery, len(goModel.Queries))
	for _, query := range goModel.Queries {
		goQueries[query.ID] = query
	}
	for _, query := range result.Queries {
		goQuery := goQueries[query.ID]
		name := queryNameRecord(generation, query.ID)
		sqlBytes, readErr := os.ReadFile(filepath.Join(root, query.SQLPath))
		if readErr != nil {
			return fmt.Errorf("schema update: read query %s: %w", query.SQLPath, readErr)
		}
		sqlText, argumentNames, lowerErr := lowerTypedSQL(string(sqlBytes), string(query.ID), query.Engine.Dialect)
		if lowerErr != nil {
			return lowerErr
		}
		parameters, pairErr := typedValues(query.Parameters, goQuery.Parameters)
		if pairErr != nil {
			return pairErr
		}
		results, pairErr := typedValues(query.Results, queryFields(goQuery))
		if pairErr != nil {
			return pairErr
		}
		store.TypedQueries = append(store.TypedQueries, generate.TypedQuery{Function: goQuery.Name, Output: name.File, Engine: query.Engine.Dialect, SQL: sqlText, ArgumentNames: argumentNames, Operation: query.Operation, Cardinality: query.Cardinality, Result: name.Result, Projection: name.Projection, Decoder: name.Decoder, Parameters: parameters, Results: results, Imports: goModel.Imports})
	}
	plan, err := store.Plan()
	if err != nil {
		return err
	}
	lock := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Source: result.Source.Record, Engine: result.Source.Engine, Catalog: compilerlock.CatalogFromPhysical(result.Catalog), Mappings: compilerlock.FromMappings(mappings), Generation: compilerlock.GenerationRecord{Package: generation.Package, Output: generation.Output, Emitter: generation.Emitter, Prune: generation.Prune}}
	for _, query := range result.Queries {
		lock.Queries = append(lock.Queries, compilerlock.QueryFromAnalysis(query))
	}
	for _, object := range generation.Objects {
		lock.Generation.Objects = append(lock.Generation.Objects, compilerlock.ObjectNameRecord{ID: string(object.ID), Source: object.Source, Row: object.Row, Create: object.Create, Patch: object.Patch, File: object.File})
	}
	for _, query := range generation.Queries {
		lock.Generation.Queries = append(lock.Generation.Queries, compilerlock.QueryNameRecord{ID: string(query.ID), Function: query.Function, Result: query.Result, Projection: query.Projection, Decoder: query.Decoder, File: query.File})
	}
	lock.Digests, err = compilerlock.BuildDigests(compilerlock.DigestInputs{Source: result.Source, Mappings: mappings, Queries: queryInputs(result.Queries), Generation: generation})
	if err != nil {
		return err
	}
	encoded, err := compilerlock.Encode(lock)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(root, "rasql.lock.json")
	lockHash := sha256.Sum256(encoded)
	oldLock, err := stateFor(lockPath)
	if err != nil {
		return err
	}
	recoveries := make([]generate.RecoveryDeletion, 0)
	if marker != nil {
		for _, entry := range marker.Entries {
			if entry.Desired.State == "missing" && entry.Path != "rasql.lock.json" {
				recoveries = append(recoveries, generate.RecoveryDeletion{Path: entry.Path, OldSHA256: entry.Old.SHA256})
			}
		}
	}
	publication := generate.Publication{
		FinalFiles:        []generate.FinalFile{{Path: "rasql.lock.json", Source: encoded, Mode: 0o600}},
		RecoveryDeletions: recoveries,
		BeforeWrite: func(_ context.Context, current []generate.PublicationEntry) error {
			if err := sourcefile.RevalidateSourceFiles(root, result.Snapshots); err != nil {
				return err
			}
			entries := pendingEntries(current)
			if marker != nil {
				return validatePendingAgainst(root, *marker, entries, hex.EncodeToString(lockHash[:]))
			}
			return writePending(root, oldLock.SHA256, hex.EncodeToString(lockHash[:]), entries)
		},
		AfterVerify: func(context.Context, []generate.PublicationEntry) error { return removePending(root) },
	}
	if c.beforePublication != nil {
		c.beforePublication()
	}
	if err := plan.CommitPublication(ctx, publication); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "updated schema and generated %s\n", cfg.Output)
	return nil
}

func queryFields(query compilerir.GoQuery) []compilerir.GoField {
	if query.Result == nil {
		return nil
	}
	return append([]compilerir.GoField(nil), query.Result.Fields...)
}

func typedValues(semantic []compilerir.SemanticValue, goFields []compilerir.GoField) ([]querygen.TypedValue, error) {
	if len(semantic) != len(goFields) {
		return nil, fmt.Errorf("typed query value count mismatch: semantic=%d go=%d", len(semantic), len(goFields))
	}
	values := make([]querygen.TypedValue, len(semantic))
	for i := range semantic {
		if semantic[i].Name != goFields[i].Name || semantic[i].Nullable != goFields[i].Nullable {
			return nil, fmt.Errorf("typed query value mismatch at %d: semantic %q go %q", i, semantic[i].Name, goFields[i].Name)
		}
		values[i] = querygen.TypedValue{Semantic: semantic[i], Go: goFields[i]}
	}
	return values, nil
}

func queryConfigFor(cfg config, id compilerir.QueryID) configQuery {
	for _, query := range cfg.Queries {
		if query.ID == id {
			return query
		}
	}
	return configQuery{}
}

func schemaObjectName(namespace, name string) schema.ObjectName {
	return schema.ObjectName{Schema: namespace, Name: name}
}

func exportGoName(name string) string {
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func queryInputs(queries []compilerir.QueryAnalysis) []compilerlock.QueryDigestInput {
	out := make([]compilerlock.QueryDigestInput, 0, len(queries))
	for _, q := range queries {
		record := compilerlock.QueryFromAnalysis(q)
		out = append(out, compilerlock.QueryDigestInput{ID: string(q.ID), SQL: record.SQL, Operation: q.Operation, Parameters: record.Parameters, Results: record.Results, Cardinality: q.Cardinality})
	}
	return out
}

func (c command) runSchemaImport(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "schema import")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "live connection string")
	baseline := flags.Bool("baseline", false, "record the current database as an explicit baseline")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Schema == nil {
		return errors.New("schema import: config requires schema")
	}
	if cfg.Schema.Kind == "live" && *dsn == "" {
		return errors.New("schema import: -dsn is required for live sources")
	}
	if cfg.Schema.Kind != "live" && !*baseline {
		return errors.New("schema import: -baseline is required for non-live sources")
	}
	if cfg.Schema.Kind == "live" && *baseline {
		return errors.New("schema import: -baseline is only valid for declared sources")
	}
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "-baseline" {
			filtered = append(filtered, arg)
		}
	}
	return c.runSchemaUpdate(filtered)
}

func (c command) runSchemaVerify(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "schema verify")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "live connection string")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Engine == nil || cfg.Schema == nil {
		return errors.New("schema verify: config requires engine and schema")
	}
	root, err := moduleRootForConfig(*configPath)
	if err != nil {
		return err
	}
	lockBytes, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
	if err != nil {
		return fmt.Errorf("schema verify: read lock: %w", err)
	}
	lock, err := compilerlock.Decode(lockBytes)
	if err != nil {
		return err
	}
	request := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: cfg.Engine.Dialect, Profile: cfg.Engine.Profile}, Source: schemasource.SchemaSourceConfig{Kind: cfg.Schema.Kind, Identity: cfg.Schema.Identity, Paths: cfg.Schema.Paths, Inputs: cfg.Schema.Inputs, Command: cfg.Schema.Command, Environment: cfg.Schema.Environment}, LiveDSN: *dsn, BootstrapDSN: *dsn, TempRoot: filepath.Join(root, ".tmp"), Scope: catalogScope(cfg)}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	verified, err := schemasource.Verify(ctx, request, schemasource.DefaultDependencies(), lock)
	if err != nil {
		return fmt.Errorf("schema verify: %w", err)
	}
	if len(verified.Differences) != 0 {
		return fmt.Errorf("schema verify: %w: %v", ErrDrift, verified.Differences)
	}
	_, _ = fmt.Fprintln(c.output, "schema is verified")
	return nil
}

func moduleRootForConfig(path string) (string, error) {
	if path != "" {
		return filepath.Abs(filepath.Dir(path))
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Abs(wd)
}

func hasErrors(diagnostics []compilerir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return true
		}
	}
	return false
}
