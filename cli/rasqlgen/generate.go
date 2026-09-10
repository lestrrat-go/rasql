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
	"time"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
)

// defaultGenerateTimeout bounds a live generate or check: opening the database, applying or
// checking migrations, reading the catalog, describing every query, and publishing the result.
const defaultGenerateTimeout = 30 * time.Second

// runGenerate renders the store package. With -dsn or -scratch it reads a live database and
// writes rasql.sum beside the generated Go; with neither, and a config still shaped as engine and
// schema, it falls through to the offline path that regenerates from rasql.lock.json unchanged.
func (c command) runGenerate(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "generate")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "connection string; required unless -scratch is set for SQLite")
	scratch := flags.Bool("scratch", false, "build a throwaway database from -dsn, apply migrations, generate, and drop it")
	timeout := flags.Duration("timeout", defaultGenerateTimeout, "generation timeout")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *dsn != "" || *scratch {
		if err := c.runGenerateFromDatabase(*configPath, settings, *dsn, *scratch, *timeout); err != nil {
			return fmt.Errorf("generate: %w", err)
		}
		return nil
	}
	if settings.Engine == nil || settings.Schema == nil {
		return errors.New("generate: config requires engine and schema, or pass -dsn or -scratch to generate from a database")
	}
	return c.runOfflineGenerate(settings, *configPath, false)
}

// runGenerateFromDatabase is the new path: it reads req.DSN (or a scratch database built from it),
// compiles and emits the store exactly as the offline path's own compile-and-emit body does, and
// commits the generated files together with one small text file, rasql.sum, in the same guarded
// publication.
func (c command) runGenerateFromDatabase(configPath string, cfg config, dsn string, scratch bool, timeout time.Duration) error {
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	lg, err := c.prepareLiveGeneration(ctx, configPath, cfg, dsn, scratch)
	if err != nil {
		return err
	}
	sum, err := buildGensumFile(cfg, lg.configDir, lg.moduleRoot, canonicalDialectName(cfg.Dialect), lg.result.Profile.ID, lg.result.Migrations, lg.plan.Files(), lg.outputAbs)
	if err != nil {
		return err
	}
	publication := generate.Publication{
		FinalFiles: []generate.FinalFile{{Path: lg.sumRelPath, Source: gensum.Encode(sum), Mode: 0o600}},
		BeforeWrite: func(context.Context, []generate.PublicationEntry) error {
			return sourcefile.RevalidateSourceFiles(lg.moduleRoot, lg.result.Snapshots)
		},
	}
	if c.beforePublication != nil {
		c.beforePublication()
	}
	if err := lg.plan.CommitPublication(ctx, publication); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "generated %s\n", cfg.Output)
	return nil
}

// liveGeneration is what runGenerateFromDatabase and runCheckLive both need after reading the
// database and rendering the store: the read result (for its profile, migrations, and source
// snapshots), the rendered plan, and the three paths every later step is resolved against.
type liveGeneration struct {
	result     schemasource.ReadResult
	plan       generate.Plan
	configDir  string
	moduleRoot string
	outputAbs  string
	// sumRelPath is where rasql.sum lives, relative to moduleRoot: the form
	// generate.Publication.FinalFiles and os.ReadFile both need.
	sumRelPath string
}

// prepareLiveGeneration is steps 1 through 7 of design section 4.1, shared by generate and check:
// load the config's paths, open or build the database, discover the profile, apply or check any
// configured migration directory, read the catalog, describe every query, and render the store.
// Nothing is written; the caller decides whether to commit the plan or merely check it.
func (c command) prepareLiveGeneration(ctx context.Context, configPath string, cfg config, dsn string, scratch bool) (liveGeneration, error) {
	if cfg.Dialect == "" {
		return liveGeneration{}, errors.New("config requires dialect")
	}
	if cfg.Package == "" || cfg.Output == "" {
		return liveGeneration{}, errors.New("config requires package and output")
	}
	configDir, err := moduleRootForConfig(configPath)
	if err != nil {
		return liveGeneration{}, err
	}
	moduleRoot := modroot.From(configDir)
	if moduleRoot == "" {
		moduleRoot = configDir
	}
	outputAbs, err := filepath.Abs(filepath.Join(configDir, filepath.FromSlash(cfg.Output)))
	if err != nil {
		return liveGeneration{}, fmt.Errorf("resolve output: %w", err)
	}
	sumRelPath, err := filepath.Rel(moduleRoot, filepath.Join(outputAbs, "rasql.sum"))
	if err != nil {
		return liveGeneration{}, fmt.Errorf("resolve rasql.sum: %w", err)
	}
	sumRelPath = filepath.ToSlash(sumRelPath)

	migrationsDir := ""
	if cfg.Migrations != "" {
		migrationsDir, err = rebaseToModuleRoot(configDir, moduleRoot, cfg.Migrations)
		if err != nil {
			return liveGeneration{}, fmt.Errorf("resolve migrations: %w", err)
		}
	}

	deps := schemasource.DefaultDependencies()
	if c.schemaDependencies != nil {
		deps = c.schemaDependencies()
	}
	if len(cfg.Queries) != 0 && deps.Analyzer == nil {
		queryConfig, qErr := cfg.compilerQueriesForModule(configDir, moduleRoot)
		if qErr != nil {
			return liveGeneration{}, qErr
		}
		analyzer, aErr := compilerquery.NewAnalyzer(queryConfig, compilerquery.Describers{PostgreSQL: querydescribe.NewPostgreSQL(), MySQL: querydescribe.NewMySQL(nil), SQLite: querydescribe.NewSQLitePrepare(nil)})
		if aErr != nil {
			return liveGeneration{}, aErr
		}
		deps.Analyzer = analyzer
	}

	req := schemasource.ReadRequest{
		ModuleRoot:    moduleRoot,
		Dialect:       cfg.Dialect,
		DSN:           dsn,
		MigrationsDir: migrationsDir,
		TempRoot:      filepath.Join(moduleRoot, ".tmp"),
		Scratch:       scratch,
		Scope:         catalogScope(cfg),
	}
	result, err := schemasource.Read(ctx, req, deps)
	if err != nil {
		return liveGeneration{}, err
	}

	var idDiagnostics []compilerir.Diagnostic
	result.Catalog, idDiagnostics = compilerir.AssignObjectIDs(result.Catalog, compilerir.IdentityInput{SourceIdentity: "live"})
	if hasErrors(idDiagnostics) {
		return liveGeneration{}, errors.New("object identity assignment failed")
	}
	mappings, err := cfg.mappings()
	if err != nil {
		return liveGeneration{}, err
	}
	semantic, diagnostics := compilerir.BuildSemantic(result.Catalog, mappings, result.Queries)
	if hasErrors(diagnostics) {
		return liveGeneration{}, errors.New("semantic analysis failed")
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
		return liveGeneration{}, err
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
		return liveGeneration{}, errors.New("go model failed")
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
		return liveGeneration{}, err
	}
	store, err := renderEmitter(input)
	if err != nil {
		return liveGeneration{}, err
	}
	store.Root = moduleRoot
	store.Dir = outputAbs
	goQueries := make(map[compilerir.QueryID]compilerir.GoQuery, len(goModel.Queries))
	for _, query := range goModel.Queries {
		goQueries[query.ID] = query
	}
	for _, query := range result.Queries {
		goQuery := goQueries[query.ID]
		name := queryNameRecord(generation, query.ID)
		sqlBytes, readErr := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(query.SQLPath)))
		if readErr != nil {
			return liveGeneration{}, fmt.Errorf("read query %s: %w", query.SQLPath, readErr)
		}
		sqlText, argumentNames, lowerErr := lowerTypedSQL(string(sqlBytes), string(query.ID), query.Engine.Dialect)
		if lowerErr != nil {
			return liveGeneration{}, lowerErr
		}
		parameters, pairErr := typedValues(query.Parameters, goQuery.Parameters)
		if pairErr != nil {
			return liveGeneration{}, pairErr
		}
		results, pairErr := typedValues(query.Results, queryFields(goQuery))
		if pairErr != nil {
			return liveGeneration{}, pairErr
		}
		store.TypedQueries = append(store.TypedQueries, generate.TypedQuery{Function: goQuery.Name, Output: name.File, Engine: query.Engine.Dialect, SQL: sqlText, ArgumentNames: argumentNames, Operation: query.Operation, Cardinality: query.Cardinality, Result: name.Result, Projection: name.Projection, Decoder: name.Decoder, Parameters: parameters, Results: results, Imports: goModel.Imports})
	}
	plan, err := store.PlanContext(ctx)
	if err != nil {
		return liveGeneration{}, err
	}
	return liveGeneration{result: result, plan: plan, configDir: configDir, moduleRoot: moduleRoot, outputAbs: outputAbs, sumRelPath: sumRelPath}, nil
}

// catalogScope builds a live catalog read's scope from the table selection settings, the way both
// the offline schema-update path and the new live path need it: it excludes the migration history
// table itself and the two companion tables migrate.Runner and migrate's plan preparation create
// beside it (history+"_progress" and history+"_plan_progress"), neither of which any project
// configures directly, so that neither is ever mistaken for a table to generate.
func catalogScope(cfg config) catalogread.Scope {
	scope := catalogread.Scope{IncludeViews: cfg.Tables.IncludeViews, Namespaces: append([]string(nil), cfg.Tables.Namespaces...)}
	for _, name := range cfg.Tables.Include {
		scope.Include = append(scope.Include, schema.ObjectName{Name: name})
	}
	for _, name := range cfg.Tables.Exclude {
		scope.Exclude = append(scope.Exclude, schema.ObjectName{Name: name})
	}
	scope.Include = append(scope.Include, cfg.Tables.IncludeObjects...)
	scope.Exclude = append(scope.Exclude, cfg.Tables.ExcludeObjects...)
	history := cfg.Tables.HistoryTable
	if history == "" {
		history = "rasql_schema_migrations"
	}
	scope.HistoryTable = schema.ObjectName{Name: history}
	scope.Exclude = append(scope.Exclude, schema.ObjectName{Name: history + "_progress"}, schema.ObjectName{Name: history + "_plan_progress"})
	return scope
}

// rebaseToModuleRoot turns relative, config-directory-relative into a path relative to
// moduleRoot instead: what schemasource.ReadRequest.MigrationsDir and compilerquery.Config's
// per-query Input are resolved against, and therefore what a query's snapshot -- and rasql.sum's
// query and migration paths -- name. relative is refused if it would resolve outside moduleRoot.
func rebaseToModuleRoot(configDir, moduleRoot, relative string) (string, error) {
	if relative == "" {
		return "", nil
	}
	abs := filepath.Join(configDir, filepath.FromSlash(relative))
	rel, err := filepath.Rel(moduleRoot, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q escapes the module root", relative)
	}
	return rel, nil
}

// compilerQueriesForModule is compilerQueries, for the live path: each query's Input is
// config-directory-relative in rasql.json, but compilerquery.Config.ModuleRoot below is the
// module root rather than the config's own directory (design section 5.4), so Input is rebased
// onto moduleRoot before the analyzer ever sees it.
func (c config) compilerQueriesForModule(configDir, moduleRoot string) (compilerquery.Config, error) {
	queries := make([]compilerquery.QueryConfig, len(c.Queries))
	for i, query := range c.Queries {
		input, err := rebaseToModuleRoot(configDir, moduleRoot, query.Input)
		if err != nil {
			return compilerquery.Config{}, fmt.Errorf("query %q input: %w", query.ID, err)
		}
		queries[i] = compilerquery.QueryConfig{ID: query.ID, Input: input, Engine: query.Engine, Function: query.Function, Output: query.Output, Operation: query.Operation, Cardinality: query.Cardinality, Parameters: query.Parameters, Results: query.Results}
	}
	mappings, err := c.mappings()
	if err != nil {
		return compilerquery.Config{}, err
	}
	return compilerquery.Config{ModuleRoot: moduleRoot, Mappings: mappings, Queries: queries}, nil
}

// buildGensumFile assembles the rasql.sum contents generate writes and check -dsn recomputes:
// the settings digest, one migration line per migration Read applied or checked (keyed by the
// checksum migrate itself records), one query line per file-backed query input, and one output
// line per file the plan renders -- read from files directly, never from disk, since files is
// exactly what Commit is about to write or Check is about to compare against.
func buildGensumFile(cfg config, configDir, moduleRoot, dialect, profile string, migrations []migrate.Migration, files []generate.File, outputAbs string) (gensum.File, error) {
	settings, err := settingsDigest(cfg)
	if err != nil {
		return gensum.File{}, err
	}
	migrationEntriesList, err := migrationEntries(migrations)
	if err != nil {
		return gensum.File{}, err
	}
	queryEntriesList, err := queryEntries(configDir, moduleRoot, cfg.Queries, false)
	if err != nil {
		return gensum.File{}, err
	}
	outputEntriesList, err := outputEntriesFromFiles(files, outputAbs)
	if err != nil {
		return gensum.File{}, err
	}
	return gensum.File{
		Dialect: dialect, Profile: profile, Settings: "sha256:" + settings,
		Migrations: migrationEntriesList, Queries: queryEntriesList, Outputs: outputEntriesList,
	}, nil
}

// migrationEntries reports one gensum.Entry per migration, keyed by its ID and valued by the
// checksum migrate.Migration.Checksum reports -- the same value migrate records in the history
// table, so a rasql.sum built from it agrees with what rasql migrate status would say about the
// same migrations.
func migrationEntries(migrations []migrate.Migration) ([]gensum.Entry, error) {
	entries := make([]gensum.Entry, 0, len(migrations))
	for _, m := range migrations {
		checksum, err := m.Checksum()
		if err != nil {
			return nil, fmt.Errorf("migration %s checksum: %w", m.ID, err)
		}
		entries = append(entries, gensum.Entry{Name: m.ID, Value: checksum})
	}
	return entries, nil
}

// queryEntries reads every query's file-backed Input, relative to configDir, and returns one
// gensum.Entry per query that declares one, named by its path relative to moduleRoot -- the same
// module-relative name the live analyzer records for it -- and valued by the sha256 of its
// current bytes. When bestEffort is set, a query whose Input cannot be resolved or read is
// skipped instead of refusing the run, so an offline check can report it as a removed query
// instead of failing outright; the live path never sets it, since schemasource.Read would already
// have refused a query file it could not read.
func queryEntries(configDir, moduleRoot string, queries []configQuery, bestEffort bool) ([]gensum.Entry, error) {
	entries := make([]gensum.Entry, 0, len(queries))
	for _, query := range queries {
		if query.Input == "" {
			continue
		}
		name, err := rebaseToModuleRoot(configDir, moduleRoot, query.Input)
		if err != nil {
			if bestEffort {
				continue
			}
			return nil, fmt.Errorf("query %q input: %w", query.ID, err)
		}
		data, readErr := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(name)))
		if readErr != nil {
			if bestEffort {
				continue
			}
			return nil, fmt.Errorf("query %q input: %w", query.ID, readErr)
		}
		digest := sha256.Sum256(data)
		entries = append(entries, gensum.Entry{Name: name, Value: "sha256:" + hex.EncodeToString(digest[:])})
	}
	return entries, nil
}

// outputEntriesFromFiles reports one gensum.Entry per rendered file, named relative to the
// output directory and valued by the sha256 of the bytes the plan already holds -- never read
// back from disk, since these are exactly the bytes Commit is about to write.
func outputEntriesFromFiles(files []generate.File, outputAbs string) ([]gensum.Entry, error) {
	entries := make([]gensum.Entry, 0, len(files))
	for _, f := range files {
		rel, err := filepath.Rel(outputAbs, f.Path)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(f.Source)
		entries = append(entries, gensum.Entry{Name: filepath.ToSlash(rel), Value: "sha256:" + hex.EncodeToString(digest[:])})
	}
	return entries, nil
}

// currentOutputEntries recomputes one gensum.Entry per name recorded, by reading that name back
// from outputAbs -- the offline counterpart of outputEntriesFromFiles, which has no rendered
// plan to read bytes from and instead reads the files a previous generate actually wrote. A name
// that no longer reads back is skipped, which is how a deleted or renamed generated file surfaces
// as an "outputs" difference rather than a read error.
func currentOutputEntries(outputAbs string, recorded []gensum.Entry) []gensum.Entry {
	entries := make([]gensum.Entry, 0, len(recorded))
	for _, e := range recorded {
		data, err := os.ReadFile(filepath.Join(outputAbs, filepath.FromSlash(e.Name)))
		if err != nil {
			continue
		}
		digest := sha256.Sum256(data)
		entries = append(entries, gensum.Entry{Name: e.Name, Value: "sha256:" + hex.EncodeToString(digest[:])})
	}
	return entries
}

// currentMigrationEntries loads every migration currently under dir (moduleRoot-relative, in
// internal/migrationdir layout) and reports one gensum.Entry per migration, the offline
// counterpart of migrationEntries when there is no live schemasource.Read result to read
// migrations from.
func currentMigrationEntries(moduleRoot, dir string) ([]gensum.Entry, error) {
	if dir == "" {
		return nil, nil
	}
	migrations, err := migrationdir.Load(filepath.Join(moduleRoot, filepath.FromSlash(dir)))
	if err != nil {
		return nil, err
	}
	return migrationEntries(migrations)
}

// formatGensumDiffs renders one "group: path" segment per difference, in Compare's own group
// order, joined for a single error message; a group with no single path (dialect, profile,
// settings) renders as its bare name.
func formatGensumDiffs(diffs []gensum.Difference) string {
	parts := make([]string, 0, len(diffs))
	for _, d := range diffs {
		if d.Path == "" {
			parts = append(parts, d.Group)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", d.Group, d.Path))
	}
	return strings.Join(parts, "; ")
}
