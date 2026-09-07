package rasqlgen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
		return fmt.Errorf("check: pending publication marker %s exists", pendingMarkerName)
	}
	lockBytes, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
	if err != nil {
		return fmt.Errorf("generate: read rasql.lock.json: %w", err)
	}
	lock, err := compilerlock.Decode(lockBytes)
	if err != nil {
		return fmt.Errorf("generate: decode rasql.lock.json: %w", err)
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
	if check {
		if err := store.Check(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(c.output, "%s is up to date\n", settings.Output)
		return nil
	}
	plan, err := store.PlanContext(context.Background())
	if err != nil {
		return err
	}
	if err := plan.Commit(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "generated %s offline\n", settings.Output)
	return nil
}
