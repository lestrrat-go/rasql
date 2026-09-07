package rasqlgen

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/namedsql"
)

func (c command) runOfflineGenerate(settings config, configPath string, check bool) error {
	root, err := moduleRootForConfig(configPath)
	if err != nil {
		return err
	}
	if _, markerErr := os.Stat(filepath.Join(root, pendingMarkerName)); markerErr == nil {
		return fmt.Errorf("%w: pending publication marker %s exists", generate.ErrStale, pendingMarkerName)
	}
	lockBytes, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
	if err != nil {
		return fmt.Errorf("generate: read rasql.lock.json: %w", err)
	}
	lock, err := compilerlock.Decode(lockBytes)
	if err != nil {
		return fmt.Errorf("generate: decode rasql.lock.json: %w", err)
	}
	groups, groupErr := offlineDigestGroups(root, settings, lock)
	if groupErr != nil {
		return groupErr
	}
	if len(groups) != 0 {
		if check {
			return fmt.Errorf("%w: %s", generate.ErrStale, strings.Join(groups, "; "))
		}
		for _, group := range groups {
			if group != "generation" {
				return fmt.Errorf("%w: %s digest differs; run schema update", generate.ErrStale, group)
			}
		}
	}
	catalog := compilerlock.PhysicalFromCatalog(lock)
	mappings, err := settings.mappings()
	if err != nil {
		return err
	}
	queries := make([]compilerir.QueryAnalysis, 0, len(lock.Queries))
	for _, query := range lock.Queries {
		queries = append(queries, compilerlock.AnalysisFromQuery(query))
	}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, mappings, queries)
	if hasErrors(diagnostics) {
		return fmt.Errorf("generate: semantic analysis failed")
	}
	prune := lock.Generation.Prune
	if settings.Prune != nil {
		prune = *settings.Prune
	}
	if settings.Package == "" {
		settings.Package = lock.Generation.Package
	}
	if settings.Output == "" {
		settings.Output = lock.Generation.Output
	}
	generation := compilerir.GoConfig{Package: settings.Package, Output: settings.Output, Emitter: lock.Generation.Emitter, Prune: prune, Scalars: mappings.Scalars}
	if generation.Emitter == "" {
		generation.Emitter = "legacy"
	}
	for _, object := range lock.Generation.Objects {
		generation.Objects = append(generation.Objects, compilerir.ObjectGoName{ID: compilerir.ObjectID(object.ID), Source: object.Source, Row: object.Row, Create: object.Create, Patch: object.Patch, File: object.File})
	}
	for _, query := range lock.Generation.Queries {
		generation.Queries = append(generation.Queries, compilerir.QueryGoName{ID: compilerir.QueryID(query.ID), Function: query.Function, Result: query.Result, Projection: query.Projection, Decoder: query.Decoder, File: query.File})
	}
	goModel, diagnostics := compilerir.BuildGo(semantic, generation)
	if hasErrors(diagnostics) {
		return fmt.Errorf("generate: Go model failed")
	}
	input, err := generate.NewEmitterInput(catalog, semantic, goModel, generation)
	if err != nil {
		return err
	}
	store, err := generate.LegacyStore(input)
	if err != nil {
		return err
	}
	store.Root = root
	store.Dir = settings.Output
	goQueries := make(map[compilerir.QueryID]compilerir.GoQuery, len(goModel.Queries))
	for _, query := range goModel.Queries {
		goQueries[query.ID] = query
	}
	querySnapshots := make([]compilerlock.SourceFileSnapshot, 0, len(lock.Queries))
	for _, query := range lock.Queries {
		goQuery := goQueries[query.ID]
		analysis := compilerlock.AnalysisFromQuery(query)
		typed := generate.TypedQuery{Function: goQuery.Name, Engine: query.Evidence.Dialect, SQL: "", Operation: query.Operation, Cardinality: query.Cardinality, Result: queryResultName(generation, query.ID), Projection: queryProjectionName(generation, query.ID), Decoder: queryDecoderName(generation, query.ID)}
		if typed.Function == "" {
			typed.Function = query.Name
		}
		typed.Output = queryFileName(generation, query.ID)
		typed.Parameters, err = typedValues(analysis.Parameters, goQuery.Parameters)
		if err != nil {
			return err
		}
		if goQuery.Result != nil {
			typed.Results, err = typedValues(analysis.Results, goQuery.Result.Fields)
			if err != nil {
				return err
			}
		}
		typed.Imports = goModel.Imports
		snapshot, readErr := compilerlock.SnapshotSourceFile(root, query.SQL.Path)
		if readErr != nil {
			return fmt.Errorf("generate: read query %s: %w", query.SQL.Path, readErr)
		}
		if snapshot.Record() != query.SQL {
			return fmt.Errorf("generate: query %s changed after lock", query.SQL.Path)
		}
		querySnapshots = append(querySnapshots, snapshot)
		typed.SQL, typed.ArgumentNames, readErr = lowerTypedSQL(string(snapshot.Bytes()), string(query.ID), query.Evidence.Dialect)
		if readErr != nil {
			return readErr
		}
		store.TypedQueries = append(store.TypedQueries, typed)
	}
	if check {
		if err := store.Check(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(c.output, "%s is up to date\n", settings.Output)
		return nil
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := store.PlanContext(ctx)
	if err != nil {
		return err
	}
	updatedLock := lock
	updatedLock.Generation.Package = generation.Package
	updatedLock.Generation.Output = generation.Output
	updatedLock.Generation.Emitter = generation.Emitter
	updatedLock.Generation.Prune = generation.Prune
	updatedDigests, digestErr := compilerlock.BuildDigests(compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: lock.Source, Engine: lock.Engine}, Mappings: mappings, Queries: lockQueryDigests(lock), Generation: generation})
	if digestErr != nil {
		return digestErr
	}
	updatedLock.Digests.Generation = updatedDigests.Generation
	encodedLock, err := compilerlock.Encode(updatedLock)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(root, "rasql.lock.json")
	oldLock, err := stateFor(lockPath)
	if err != nil {
		return err
	}
	lockSum := fmt.Sprintf("%x", sha256.Sum256(encodedLock))
	publication := generate.Publication{
		FinalFiles: []generate.FinalFile{{Path: "rasql.lock.json", Source: encodedLock, Mode: 0o600}},
		BeforeWrite: func(_ context.Context, entries []generate.PublicationEntry) error {
			if err := compilerlock.RevalidateSourceFiles(root, querySnapshots); err != nil {
				return err
			}
			return writePending(root, oldLock.SHA256, lockSum, pendingEntries(entries))
		},
		AfterVerify: func(context.Context, []generate.PublicationEntry) error { return removePending(root) },
	}
	if c.beforePublication != nil {
		c.beforePublication()
	}
	if err := plan.CommitPublication(ctx, publication); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "generated %s offline\n", settings.Output)
	return nil
}

func lowerTypedSQL(source, name, engine string) (string, []string, error) {
	template, err := namedsql.Parse(name, source)
	if err != nil {
		return "", nil, err
	}
	var sqlDialect dialect.Dialect
	switch engine {
	case "postgresql", "postgres":
		sqlDialect = dialect.PostgreSQL()
	case "mysql":
		sqlDialect = dialect.MySQL()
	default:
		sqlDialect = dialect.SQLite()
	}
	compiled, err := template.Compile(sqlDialect)
	if err != nil {
		return "", nil, err
	}
	definition := compiled.QueryDef()
	return definition.SQL, definition.Parameters, nil
}

func offlineDigestGroups(root string, settings config, lock compilerlock.File) ([]string, error) {
	mappings, err := settings.mappings()
	if err != nil {
		return nil, err
	}
	source := compilerlock.SourceDigestInput{Record: lock.Source, Engine: lock.Engine}
	source.Record.Files = nil
	sourceChanged := false
	for _, file := range lock.Source.Files {
		snapshot, snapshotErr := compilerlock.SnapshotSourceFile(root, file.Path)
		if snapshotErr != nil {
			sourceChanged = true
			continue
		}
		if snapshot.Record().SHA256 != file.SHA256 {
			sourceChanged = true
		}
		source.Record.Files = append(source.Record.Files, snapshot.Record())
	}
	queries := make([]compilerlock.QueryDigestInput, 0, len(lock.Queries))
	queryChanged := false
	for _, query := range lock.Queries {
		input := query.SQL
		if snapshot, snapshotErr := compilerlock.SnapshotSourceFile(root, query.SQL.Path); snapshotErr == nil {
			input.SHA256 = snapshot.Record().SHA256
		} else {
			queryChanged = true
		}
		if input.SHA256 != query.SQL.SHA256 {
			queryChanged = true
		}
		current := query
		for _, configured := range settings.Queries {
			if configured.ID != query.ID {
				continue
			}
			current.Operation = configured.Operation
			current.Cardinality = configured.Cardinality
			current.Parameters = configValueRecords(configured.Parameters, mappings)
			current.Results = configValueRecords(configured.Results, mappings)
		}
		queries = append(queries, compilerlock.QueryDigestInput{ID: string(query.ID), SQL: input, Operation: current.Operation, Parameters: current.Parameters, Results: current.Results, Cardinality: current.Cardinality})
	}
	generation := compilerir.GoConfig{Package: lock.Generation.Package, Output: lock.Generation.Output, Emitter: lock.Generation.Emitter, Prune: lock.Generation.Prune}
	if settings.Package != "" {
		generation.Package = settings.Package
	}
	if settings.Output != "" {
		generation.Output = settings.Output
	}
	if settings.Prune != nil {
		generation.Prune = *settings.Prune
	}
	for _, object := range lock.Generation.Objects {
		generation.Objects = append(generation.Objects, compilerir.ObjectGoName{ID: compilerir.ObjectID(object.ID), Source: object.Source, Row: object.Row, Create: object.Create, Patch: object.Patch, File: object.File})
	}
	for _, query := range lock.Generation.Queries {
		generation.Queries = append(generation.Queries, compilerir.QueryGoName{ID: compilerir.QueryID(query.ID), Function: query.Function, Result: query.Result, Projection: query.Projection, Decoder: query.Decoder, File: query.File})
	}
	digests, err := compilerlock.BuildDigests(compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: lock.Source, Engine: lock.Engine}, Mappings: mappings, Queries: queries, Generation: generation})
	if err != nil {
		return nil, err
	}
	groups := make([]string, 0, 4)
	if sourceChanged {
		groups = append(groups, "source")
	}
	if queryChanged {
		groups = append(groups, "queries")
	}
	if digests.Mappings != lock.Digests.Mappings {
		groups = append(groups, "mappings")
	}
	if digests.Queries != lock.Digests.Queries {
		groups = append(groups, "queries")
	}
	if digests.Generation != lock.Digests.Generation {
		groups = append(groups, "generation")
	}
	sort.Strings(groups)
	groups = slices.Compact(groups)
	return groups, nil
}

func configValueRecords(values []compilerquery.ValueDeclaration, mappings compilerir.MappingConfig) []compilerlock.ValueRecord {
	if values == nil {
		return nil
	}
	out := make([]compilerlock.ValueRecord, len(values))
	for i, value := range values {
		nullable := value.Nullable != nil && *value.Nullable
		logicalKind, _, _ := compilerir.DeclaredQueryLogicalKind(value.Scalar, mappings)
		out[i] = compilerlock.ValueRecord{Name: value.Name, Scalar: value.Scalar, Nullable: nullable, TypeCertainty: compilerir.CertaintyDeclared, NullabilityCertainty: compilerir.CertaintyDeclared, LogicalKind: logicalKind}
	}
	return out
}

func lockQueryDigests(lock compilerlock.File) []compilerlock.QueryDigestInput {
	out := make([]compilerlock.QueryDigestInput, 0, len(lock.Queries))
	for _, query := range lock.Queries {
		out = append(out, compilerlock.QueryDigestInput{ID: string(query.ID), SQL: query.SQL, Operation: query.Operation, Parameters: query.Parameters, Results: query.Results, Cardinality: query.Cardinality})
	}
	return out
}

func queryNameRecord(config compilerir.GoConfig, id compilerir.QueryID) compilerlock.QueryNameRecord {
	for _, query := range config.Queries {
		if query.ID == id {
			return compilerlock.QueryNameRecord{ID: string(query.ID), Function: query.Function, Result: query.Result, Projection: query.Projection, Decoder: query.Decoder, File: query.File}
		}
	}
	return compilerlock.QueryNameRecord{}
}
func queryResultName(config compilerir.GoConfig, id compilerir.QueryID) string {
	return queryNameRecord(config, id).Result
}
func queryProjectionName(config compilerir.GoConfig, id compilerir.QueryID) string {
	return queryNameRecord(config, id).Projection
}
func queryDecoderName(config compilerir.GoConfig, id compilerir.QueryID) string {
	return queryNameRecord(config, id).Decoder
}
func queryFileName(config compilerir.GoConfig, id compilerir.QueryID) string {
	return queryNameRecord(config, id).File
}
