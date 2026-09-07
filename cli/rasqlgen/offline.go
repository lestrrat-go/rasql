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

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
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
			return writePending(root, oldLock.SHA256, lockSum, pendingEntries(entries))
		},
		AfterVerify: func(context.Context, []generate.PublicationEntry) error { return removePending(root) },
	}
	if err := plan.CommitPublication(ctx, publication); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "generated %s offline\n", settings.Output)
	return nil
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
		queries = append(queries, compilerlock.QueryDigestInput{ID: string(query.ID), SQL: input, Operation: query.Operation, Parameters: query.Parameters, Results: query.Results, Cardinality: query.Cardinality})
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

func lockQueryDigests(lock compilerlock.File) []compilerlock.QueryDigestInput {
	out := make([]compilerlock.QueryDigestInput, 0, len(lock.Queries))
	for _, query := range lock.Queries {
		out = append(out, compilerlock.QueryDigestInput{ID: string(query.ID), SQL: query.SQL, Operation: query.Operation, Parameters: query.Parameters, Results: query.Results, Cardinality: query.Cardinality})
	}
	return out
}
