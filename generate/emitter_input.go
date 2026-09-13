package generate

import (
	"fmt"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

// EmitterInput is the complete, sidecar-free input to a generated store: the
// physical catalog a store is generated from, the mapping and generation
// policy applied to it, and the semantic and Go models those three produce.
//
// NewEmitterInput derives the semantic and Go models rather than accepting
// them, so the models an emitter reads always describe the catalog beside
// them. The fields are unexported for the same reason: a caller that could
// replace one model could make it disagree with the rest.
type EmitterInput struct {
	catalog    compilerir.PhysicalCatalog
	semantic   compilerir.SemanticModel
	goModel    compilerir.GoModel
	mappings   compilerir.MappingConfig
	generation compilerir.GoConfig
}

// NewEmitterInput derives the semantic and Go models for catalog under mappings
// and generation, and returns the emitter input carrying all five values.
//
// queries are the analyzed typed queries to generate beside the catalog's
// objects; pass none to generate objects only. Every argument is copied, so a
// caller may keep mutating what it passed.
func NewEmitterInput(catalog compilerir.PhysicalCatalog, mappings compilerir.MappingConfig, generation compilerir.GoConfig, queries ...compilerir.QueryAnalysis) (EmitterInput, error) {
	in := EmitterInput{catalog: catalog.Clone(), mappings: mappings.Clone(), generation: generation.Clone()}
	if err := compilerir.ValidatePhysical(in.catalog); err != nil {
		return EmitterInput{}, fmt.Errorf("generate: emitter catalog: %w", err)
	}
	if in.generation.Emitter != "legacy" && in.generation.Emitter != "compact" {
		return EmitterInput{}, fmt.Errorf("generate: emitter generation.emitter must be compact or legacy")
	}
	if in.generation.Package == "" || in.generation.Output == "" {
		return EmitterInput{}, fmt.Errorf("generate: emitter generation package and output are required")
	}
	if err := compilerir.ValidateMappingConfig(in.mappings, in.generation.Package); err != nil {
		return EmitterInput{}, fmt.Errorf("generate: emitter generation mappings: %w", err)
	}
	// BuildGo reads its scalar mappings from the generation config and
	// BuildSemantic reads them from the mapping config. Filling the one from the
	// other is what binds both models to a single set of mappings.
	in.generation.Scalars = in.mappings.Clone().Scalars

	analyzed := make([]compilerir.QueryAnalysis, len(queries))
	for i, query := range queries {
		analyzed[i] = query.Clone()
	}
	semantic, diagnostics := compilerir.BuildSemantic(in.catalog, in.mappings, analyzed)
	if err := firstEmitterDiagnostic("semantic", diagnostics); err != nil {
		return EmitterInput{}, err
	}
	if err := compilerir.ValidateSemantic(semantic); err != nil {
		return EmitterInput{}, fmt.Errorf("generate: emitter semantic: %w", err)
	}
	in.semantic = semantic

	goModel, diagnostics := compilerir.BuildGo(in.semantic, in.generation)
	if err := firstEmitterDiagnostic("Go", diagnostics); err != nil {
		return EmitterInput{}, err
	}
	if err := compilerir.ValidateGo(goModel); err != nil {
		return EmitterInput{}, fmt.Errorf("generate: emitter Go model: %w", err)
	}
	in.goModel = goModel

	if err := in.validateGenerationPolicy(); err != nil {
		return EmitterInput{}, err
	}
	return in, nil
}

// GoModel returns a copy of the Go model derived for this input. A caller that
// generates more than the catalog's own objects -- typed query source, say --
// needs the Go names and imports the emitter will use, and this is how it reads
// them without rebuilding the model for itself.
func (in EmitterInput) GoModel() compilerir.GoModel {
	return in.goModel.Clone()
}

// Emitter reports which emitter this input was built for, so a caller that
// dispatches between emitters reads the choice off the input it is about to
// render rather than off a config value beside it.
func (in EmitterInput) Emitter() string {
	return in.generation.Emitter
}

func (in EmitterInput) clone() EmitterInput {
	return EmitterInput{
		catalog:    in.catalog.Clone(),
		semantic:   in.semantic.Clone(),
		goModel:    in.goModel.Clone(),
		mappings:   in.mappings.Clone(),
		generation: in.generation.Clone(),
	}
}

func firstEmitterDiagnostic(stage string, diagnostics []compilerir.Diagnostic) error {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return fmt.Errorf("generate: emitter %s diagnostics: %s", stage, diagnostic.Message)
		}
	}
	return nil
}

// validateGenerationPolicy checks what the model builders do not: that the
// generation policy names exactly the catalog's objects, that it names real
// queries, and that every output file it asks for exists in the Go model.
func (in EmitterInput) validateGenerationPolicy() error {
	physical := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(in.catalog.Objects))
	for _, object := range in.catalog.Objects {
		physical[object.ID] = object
	}
	configured := make(map[compilerir.ObjectID]struct{}, len(in.generation.Objects))
	for _, object := range in.generation.Objects {
		if _, ok := physical[object.ID]; !ok {
			return fmt.Errorf("generate: emitter generation object %q is absent from catalog", object.ID)
		}
		if _, ok := configured[object.ID]; ok {
			return fmt.Errorf("generate: emitter generation object %q is duplicated", object.ID)
		}
		configured[object.ID] = struct{}{}
	}
	if len(configured) != len(physical) {
		return fmt.Errorf("generate: emitter generation policy does not cover every catalog object")
	}
	if err := in.validateQueryPolicy(); err != nil {
		return err
	}
	return in.validateGenerationFiles(physical)
}

func (in EmitterInput) validateQueryPolicy() error {
	semantic := make(map[compilerir.QueryID]compilerir.SemanticQuery, len(in.semantic.Queries))
	for _, query := range in.semantic.Queries {
		semantic[query.ID] = query
	}
	for _, cfg := range in.generation.Queries {
		if _, ok := semantic[cfg.ID]; !ok {
			return fmt.Errorf("generate: emitter generation query %q is absent from semantic model", cfg.ID)
		}
	}
	// BuildGo gives a query a result shape only when the analysis reported
	// result values, so a reading query whose analysis found none would emit a
	// decoder with nothing to decode into.
	for _, query := range in.goModel.Queries {
		if query.Cardinality == "exec" && query.Result != nil || query.Cardinality != "exec" && query.Result == nil {
			return fmt.Errorf("generate: emitter query %q result shape disagrees with cardinality", query.ID)
		}
	}
	return nil
}

func (in EmitterInput) validateGenerationFiles(physical map[compilerir.ObjectID]compilerir.PhysicalObject) error {
	files := make(map[string]struct{}, len(in.goModel.Files))
	for _, file := range in.goModel.Files {
		files[file.Path] = struct{}{}
	}
	for _, cfg := range in.generation.Objects {
		if cfg.File == "" {
			continue
		}
		if _, ok := files[cfg.File]; !ok {
			return fmt.Errorf("generate: emitter object %q file %q is absent from Go model", cfg.ID, cfg.File)
		}
		if physical[cfg.ID].Name == "" {
			return fmt.Errorf("generate: emitter object %q has no physical name", cfg.ID)
		}
	}
	for _, cfg := range in.generation.Queries {
		if cfg.File == "" {
			continue
		}
		if _, ok := files[cfg.File]; !ok {
			return fmt.Errorf("generate: emitter query %q file %q is absent from Go model", cfg.ID, cfg.File)
		}
	}
	return nil
}
