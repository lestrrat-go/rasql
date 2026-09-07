package generate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
)

// EmitterInput is the complete, sidecar-free input to a generated store.
// It is intentionally not part of the lock wire format.
type EmitterInput struct {
	Catalog    compilerir.PhysicalCatalog
	Semantic   compilerir.SemanticModel
	Go         compilerir.GoModel
	Generation compilerir.GoConfig
}

func NewEmitterInput(catalog compilerir.PhysicalCatalog, semantic compilerir.SemanticModel, goModel compilerir.GoModel, generation compilerir.GoConfig) (EmitterInput, error) {
	in := EmitterInput{Catalog: catalog.Clone(), Semantic: semantic.Clone(), Go: goModel.Clone(), Generation: generation.Clone()}
	if err := in.Validate(); err != nil {
		return EmitterInput{}, err
	}
	return in, nil
}

func (in EmitterInput) Clone() EmitterInput {
	return EmitterInput{Catalog: in.Catalog.Clone(), Semantic: in.Semantic.Clone(), Go: in.Go.Clone(), Generation: in.Generation.Clone()}
}

// Validate checks both component validity and agreement between compiler layers.
func (in EmitterInput) Validate() error {
	if err := compilerir.ValidatePhysical(in.Catalog); err != nil {
		return fmt.Errorf("generate: emitter catalog: %w", err)
	}
	if err := compilerir.ValidateSemantic(in.Semantic); err != nil {
		return fmt.Errorf("generate: emitter semantic: %w", err)
	}
	if err := compilerir.ValidateGo(in.Go); err != nil {
		return fmt.Errorf("generate: emitter Go model: %w", err)
	}
	if in.Generation.Emitter != "legacy" {
		return fmt.Errorf("generate: emitter generation.emitter must be legacy")
	}
	if in.Generation.Package == "" || in.Generation.Output == "" {
		return fmt.Errorf("generate: emitter generation package and output are required")
	}
	if in.Go.Package != in.Generation.Package {
		return fmt.Errorf("generate: emitter Go package %q disagrees with generation package %q", in.Go.Package, in.Generation.Package)
	}
	objects := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(in.Catalog.Objects))
	for _, object := range in.Catalog.Objects {
		objects[object.ID] = object
	}
	semantic := make(map[compilerir.ObjectID]compilerir.SemanticObject, len(in.Semantic.Objects))
	for _, object := range in.Semantic.Objects {
		physical, ok := objects[object.ID]
		if !ok {
			return fmt.Errorf("generate: emitter semantic object %q is absent from catalog", object.ID)
		}
		if object.PhysicalName.Schema != physical.Schema || object.PhysicalName.Name != physical.Name {
			return fmt.Errorf("generate: emitter semantic object %q disagrees with catalog name", object.ID)
		}
		if len(object.Columns) != len(physical.Columns) {
			return fmt.Errorf("generate: emitter semantic object %q column count disagrees with catalog", object.ID)
		}
		for i, column := range object.Columns {
			if column.Name != physical.Columns[i].Name || column.Nullable != physical.Columns[i].Nullable {
				return fmt.Errorf("generate: emitter semantic object %q column %d disagrees with catalog", object.ID, i)
			}
		}
		semantic[object.ID] = object
	}
	if len(semantic) != len(objects) {
		return fmt.Errorf("generate: emitter semantic model does not cover every catalog object")
	}
	goObjects := make(map[compilerir.ObjectID]compilerir.GoObject, len(in.Go.Objects))
	for _, object := range in.Go.Objects {
		physical, ok := objects[object.ID]
		if !ok {
			return fmt.Errorf("generate: emitter Go object %q is absent from catalog", object.ID)
		}
		if _, ok := semantic[object.ID]; !ok {
			return fmt.Errorf("generate: emitter Go object %q is absent from semantic model", object.ID)
		}
		if object.SourceName == "" || object.Row.Name == "" {
			return fmt.Errorf("generate: emitter Go object %q has incomplete names", object.ID)
		}
		if len(object.Columns) != len(physical.Columns) {
			return fmt.Errorf("generate: emitter Go object %q column count disagrees with catalog", object.ID)
		}
		for i, column := range object.Columns {
			if column.PhysicalName != physical.Columns[i].Name {
				return fmt.Errorf("generate: emitter Go object %q column %d disagrees with catalog", object.ID, i)
			}
			logical := semantic[object.ID].Columns[i]
			if column.Scalar != logical.Scalar || column.Nullable != logical.Nullable || column.InsertState != logical.InsertState || column.PatchState != logical.PatchState {
				return fmt.Errorf("generate: emitter Go object %q column %d disagrees with semantic model", object.ID, i)
			}
		}
		goObjects[object.ID] = object
	}
	if len(goObjects) != len(objects) {
		return fmt.Errorf("generate: emitter Go model does not cover every catalog object")
	}
	for _, object := range in.Generation.Objects {
		if _, ok := objects[compilerir.ObjectID(object.ID)]; !ok {
			return fmt.Errorf("generate: emitter generation object %q is absent from catalog", object.ID)
		}
	}
	return nil
}

// LegacyStore adapts canonical facts to the existing legacy renderer.
func LegacyStore(in EmitterInput) (Store, error) {
	if err := in.Validate(); err != nil {
		return Store{}, err
	}
	tables, diagnostics := compilerir.TableDefsFromPhysical(in.Catalog)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Store{}, fmt.Errorf("generate: emitter: %s", diagnostic.Message)
		}
	}
	goByID := make(map[compilerir.ObjectID]compilerir.GoObject, len(in.Go.Objects))
	for _, object := range in.Go.Objects {
		goByID[object.ID] = object
	}
	semanticByID := make(map[compilerir.ObjectID]compilerir.SemanticObject, len(in.Semantic.Objects))
	for _, object := range in.Semantic.Objects {
		semanticByID[object.ID] = object
	}
	physicalByID := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(in.Catalog.Objects))
	for _, object := range in.Catalog.Objects {
		physicalByID[object.ID] = object
	}
	for i := range tables {
		id := in.Catalog.Objects[i].ID
		goObject := goByID[id]
		tables[i].RowName = goObject.Row.Name
		if tables[i].RowName != "" {
			tables[i].RowName = strings.ToUpper(tables[i].RowName[:1]) + tables[i].RowName[1:]
		}
		tables[i].Operations = schema.OperationRead | schema.OperationInsert | schema.OperationUpdate | schema.OperationDelete | schema.OperationDDL
		for _, relation := range semanticByID[id].Relations {
			target := physicalByID[relation.Target]
			optionality := schema.RelationshipOptionalityInferred
			if relation.Nullable {
				optionality = schema.RelationshipOptional
			} else {
				optionality = schema.RelationshipRequired
			}
			tables[i].Relationships = append(tables[i].Relationships, schema.RelationshipDef{Name: relation.Name, Kind: schema.RelationshipKind(relation.Kind), Optionality: optionality, Columns: slices.Clone(relation.From), ReferencedSchema: target.Schema, ReferencedTable: target.Name, ReferencedColumns: slices.Clone(relation.To)})
		}
		for j := range tables[i].Columns {
			column := goObject.Columns[j]
			tables[i].Columns[j].GoBinding = &schema.GoBinding{Type: column.GoType, NullableType: column.GoType, Imports: make([]schema.GoImport, len(in.Go.Imports))}
			for k, imp := range in.Go.Imports {
				tables[i].Columns[j].GoBinding.Imports[k] = schema.GoImport{Path: imp.Path, Name: imp.Alias}
			}
		}
	}
	return Store{Package: in.Generation.Package, Root: "", Dir: in.Generation.Output, Tables: tables, Prune: in.Generation.Prune, Dialect: generationDialect(in.Catalog.Engine.Dialect)}, nil
}

func generationDialect(name string) dialect.Dialect {
	switch name {
	case "postgresql":
		return dialect.PostgreSQL()
	case "mysql":
		return dialect.MySQL()
	default:
		return dialect.SQLite()
	}
}
