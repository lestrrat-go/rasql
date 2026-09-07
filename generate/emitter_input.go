package generate

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

// EmitterInput is the complete, sidecar-free input to a generated store.
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

// Validate checks each compiler layer and their canonical agreement.
func (in EmitterInput) Validate() error {
	if err := compilerir.ValidatePhysical(in.Catalog); err != nil {
		return fmt.Errorf("generate: emitter catalog: %w", err)
	}
	if err := compilerir.ValidateSemantic(in.Semantic); err != nil {
		return fmt.Errorf("generate: emitter semantic: %w", err)
	}
	if err := validateGoModel(in.Go, in.Catalog); err != nil {
		return fmt.Errorf("generate: emitter Go model: %w", err)
	}
	if in.Generation.Emitter != "legacy" {
		return fmt.Errorf("generate: emitter generation.emitter must be legacy")
	}
	if in.Generation.Package == "" || in.Generation.Output == "" {
		return fmt.Errorf("generate: emitter generation package and output are required")
	}
	if err := compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: in.Generation.Scalars}, in.Generation.Package); err != nil {
		return fmt.Errorf("generate: emitter generation mappings: %w", err)
	}
	if len(in.Generation.Queries) != 0 || len(in.Semantic.Queries) != 0 || len(in.Go.Queries) != 0 {
		return fmt.Errorf("generate: legacy emitter cannot represent queries")
	}
	if in.Go.Package != in.Generation.Package {
		return fmt.Errorf("generate: emitter Go package %q disagrees with generation package %q", in.Go.Package, in.Generation.Package)
	}
	canonicalSemantic, diagnostics := compilerir.BuildSemantic(in.Catalog, compilerir.MappingConfig{Scalars: in.Generation.Scalars}, nil)
	if len(diagnostics) != 0 {
		return fmt.Errorf("generate: emitter canonical semantic diagnostics: %v", diagnostics)
	}
	if !reflect.DeepEqual(canonicalSemantic.Objects, in.Semantic.Objects) {
		return fmt.Errorf("generate: emitter semantic model disagrees with catalog-derived policy")
	}

	physical := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(in.Catalog.Objects))
	for _, object := range in.Catalog.Objects {
		physical[object.ID] = object
	}
	semantic := make(map[compilerir.ObjectID]compilerir.SemanticObject, len(in.Semantic.Objects))
	for _, object := range in.Semantic.Objects {
		p, ok := physical[object.ID]
		if !ok {
			return fmt.Errorf("generate: emitter semantic object %q is absent from catalog", object.ID)
		}
		if object.Kind != p.Kind || object.PhysicalName.Schema != p.Schema || object.PhysicalName.Name != p.Name {
			return fmt.Errorf("generate: emitter semantic object %q disagrees with catalog identity", object.ID)
		}
		if len(object.Columns) != len(p.Columns) {
			return fmt.Errorf("generate: emitter semantic object %q column count disagrees with catalog", object.ID)
		}
		for i, column := range object.Columns {
			if column.Name != p.Columns[i].Name || column.Nullable != p.Columns[i].Nullable {
				return fmt.Errorf("generate: emitter semantic object %q column %d disagrees with catalog", object.ID, i)
			}
		}
		semantic[object.ID] = object
	}
	if len(semantic) != len(physical) {
		return fmt.Errorf("generate: emitter semantic model does not cover every catalog object")
	}

	configured := make(map[compilerir.ObjectID]compilerir.ObjectGoName, len(in.Generation.Objects))
	for _, object := range in.Generation.Objects {
		if _, ok := physical[object.ID]; !ok {
			return fmt.Errorf("generate: emitter generation object %q is absent from catalog", object.ID)
		}
		if _, ok := configured[object.ID]; ok {
			return fmt.Errorf("generate: emitter generation object %q is duplicated", object.ID)
		}
		configured[object.ID] = object
	}
	if len(configured) != len(physical) {
		return fmt.Errorf("generate: emitter generation policy does not cover every catalog object")
	}

	goObjects := make(map[compilerir.ObjectID]struct{}, len(in.Go.Objects))
	for _, object := range in.Go.Objects {
		p, ok := physical[object.ID]
		if !ok {
			return fmt.Errorf("generate: emitter Go object %q is absent from catalog", object.ID)
		}
		s, ok := semantic[object.ID]
		if !ok {
			return fmt.Errorf("generate: emitter Go object %q is absent from semantic model", object.ID)
		}
		if _, ok := goObjects[object.ID]; ok {
			return fmt.Errorf("generate: emitter Go object %q is duplicated", object.ID)
		}
		if err := validateGoObject(object, p, s, configured[object.ID], in.Generation.Scalars); err != nil {
			return err
		}
		goObjects[object.ID] = struct{}{}
	}
	if len(goObjects) != len(physical) {
		return fmt.Errorf("generate: emitter Go model does not cover every catalog object")
	}
	return validateGenerationFiles(in.Go, in.Generation, physical)
}

func validateGoObject(object compilerir.GoObject, physical compilerir.PhysicalObject, semantic compilerir.SemanticObject, cfg compilerir.ObjectGoName, mappings []compilerir.ScalarMapping) error {
	if object.SourceName == "" || object.Row.Name == "" {
		return fmt.Errorf("generate: emitter Go object %q has incomplete names", object.ID)
	}
	if physical.Kind != "view" && (object.Create == nil || object.Patch == nil) {
		return fmt.Errorf("generate: emitter Go object %q has incomplete write shapes", object.ID)
	}
	if cfg.Source != "" && cfg.Source != object.SourceName || cfg.Row != "" && cfg.Row != object.Row.Name || cfg.Create != "" && cfg.Create != object.Create.Name || cfg.Patch != "" && cfg.Patch != object.Patch.Name {
		return fmt.Errorf("generate: emitter Go object %q disagrees with generation names", object.ID)
	}
	if len(object.Columns) != len(physical.Columns) || len(object.Columns) != len(semantic.Columns) {
		return fmt.Errorf("generate: emitter Go object %q column count disagrees with canonical models", object.ID)
	}
	for i, column := range object.Columns {
		logical, physicalColumn := semantic.Columns[i], physical.Columns[i]
		if column.Name != logical.Name || column.PhysicalName != physicalColumn.Name || column.Scalar != logical.Scalar || column.Nullable != logical.Nullable || column.InsertState != logical.InsertState || column.PatchState != logical.PatchState {
			return fmt.Errorf("generate: emitter Go object %q column %d disagrees with canonical models", object.ID, i)
		}
		wantType, wantCodec, ok := expectedBinding(column.Scalar, column.Nullable, mappings)
		if !ok || column.GoType != wantType || column.Codec != wantCodec {
			return fmt.Errorf("generate: emitter Go object %q column %q binding disagrees with mapping", object.ID, column.Name)
		}
	}
	if len(object.Relations) != len(semantic.Relations) {
		return fmt.Errorf("generate: emitter Go object %q relation count disagrees with semantic model", object.ID)
	}
	for i, relation := range object.Relations {
		want := semantic.Relations[i]
		if relation.Name != want.Name || relation.Target != want.Target || relation.Kind != want.Kind || relation.Nullable != want.Nullable {
			return fmt.Errorf("generate: emitter Go object %q relation %d disagrees with semantic model", object.ID, i)
		}
	}
	if err := validateShapeFields(object, &object.Row, semantic.Columns, func(column compilerir.SemanticColumn) bool { return column.Readable }); err != nil {
		return err
	}
	if object.Create != nil {
		if err := validateShapeFields(object, object.Create, semantic.Columns, func(column compilerir.SemanticColumn) bool {
			return column.InsertState != "forbidden" && column.InsertState != "generated"
		}); err != nil {
			return err
		}
	}
	if object.Patch != nil {
		if err := validateShapeFields(object, object.Patch, semantic.Columns, func(column compilerir.SemanticColumn) bool { return column.PatchState != "forbidden" }); err != nil {
			return err
		}
	}
	return nil
}

func validateGoModel(model compilerir.GoModel, catalog compilerir.PhysicalCatalog) error {
	copy := model.Clone()
	byID := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(catalog.Objects))
	for _, object := range catalog.Objects {
		byID[object.ID] = object
	}
	for i := range copy.Objects {
		if byID[copy.Objects[i].ID].Kind != "view" {
			continue
		}
		if copy.Objects[i].Create == nil {
			copy.Objects[i].Create = &compilerir.GoShape{Name: copy.Objects[i].SourceName + "Create"}
		}
		if copy.Objects[i].Patch == nil {
			copy.Objects[i].Patch = &compilerir.GoShape{Name: copy.Objects[i].SourceName + "Patch"}
		}
	}
	return compilerir.ValidateGo(copy)
}

func validateShapeFields(object compilerir.GoObject, shape *compilerir.GoShape, columns []compilerir.SemanticColumn, include func(compilerir.SemanticColumn) bool) error {
	want := make([]compilerir.SemanticColumn, 0, len(columns))
	for _, column := range columns {
		if include(column) {
			want = append(want, column)
		}
	}
	if len(shape.Fields) != len(want) {
		return fmt.Errorf("generate: emitter Go object %q shape %q field coverage disagrees with semantic model", object.ID, shape.Name)
	}
	for i, field := range shape.Fields {
		column := want[i]
		goColumn, ok := columnByName(object.Columns, column.Name)
		if !ok || field.Name != column.Name || field.Type != goColumn.GoType || field.Codec != goColumn.Codec || field.Nullable != goColumn.Nullable {
			return fmt.Errorf("generate: emitter Go object %q shape %q field %q disagrees with canonical models", object.ID, shape.Name, field.Name)
		}
	}
	return nil
}

func expectedBinding(scalar string, nullable bool, mappings []compilerir.ScalarMapping) (string, string, bool) {
	if mapping, ok := mappingFor(scalar, mappings); ok {
		typeName := mapping.GoType
		if nullable {
			typeName = mapping.NullableGoType
			if typeName == "" {
				typeName = "rasql.Nullable[" + mapping.GoType + "]"
			}
		}
		return typeName, mapping.Codec, true
	}
	mapping, ok := compilerir.DefaultScalarMapping(scalar)
	if !ok {
		return "", "", false
	}
	if nullable {
		if scalar == "time" {
			return "rasql.Nullable[time.Time]", "", true
		}
		return "rasql.Nullable[" + mapping.GoType + "]", "", true
	}
	return mapping.GoType, "", true
}

func columnByName(columns []compilerir.GoColumn, name string) (compilerir.GoColumn, bool) {
	for _, column := range columns {
		if column.Name == name {
			return column, true
		}
	}
	return compilerir.GoColumn{}, false
}

func validateGenerationFiles(goModel compilerir.GoModel, generation compilerir.GoConfig, physical map[compilerir.ObjectID]compilerir.PhysicalObject) error {
	files := make(map[string]struct{}, len(goModel.Files))
	for _, file := range goModel.Files {
		files[file.Path] = struct{}{}
	}
	for _, cfg := range generation.Objects {
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
	for _, mapping := range in.Generation.Scalars {
		if mapping.Codec == "" {
			continue
		}
		for _, object := range in.Semantic.Objects {
			for _, column := range object.Columns {
				if column.Scalar == mapping.Name {
					return Store{}, fmt.Errorf("generate: emitter legacy route cannot represent codec %q for %s.%s", mapping.Codec, object.PhysicalName.Name, column.Name)
				}
			}
		}
	}
	goByID := make(map[compilerir.ObjectID]compilerir.GoObject, len(in.Go.Objects))
	semanticByID := make(map[compilerir.ObjectID]compilerir.SemanticObject, len(in.Semantic.Objects))
	physicalByID := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(in.Catalog.Objects))
	for _, object := range in.Go.Objects {
		goByID[object.ID] = object
	}
	for _, object := range in.Semantic.Objects {
		semanticByID[object.ID] = object
	}
	for _, object := range in.Catalog.Objects {
		physicalByID[object.ID] = object
	}
	names := make(map[schema.ObjectName]ObjectNames, len(tables))
	configByID := make(map[compilerir.ObjectID]compilerir.ObjectGoName, len(in.Generation.Objects))
	for _, cfg := range in.Generation.Objects {
		configByID[cfg.ID] = cfg
	}
	for i := range tables {
		id := in.Catalog.Objects[i].ID
		goObject := goByID[id]
		cfg := configByID[id]
		names[tables[i].ObjectName()] = ObjectNames{Accessor: cfg.Source, RowType: cfg.Row}
		if cfg.File != "" {
			names[tables[i].ObjectName()] = ObjectNames{Accessor: cfg.Source, RowType: cfg.Row, FileBase: strings.TrimSuffix(cfg.File, "_gen.go")}
		}
		for j := range tables[i].Columns {
			column := goObject.Columns[j]
			if mapping, ok := mappingFor(column.Scalar, in.Generation.Scalars); ok {
				nullableType := mapping.NullableGoType
				if nullableType == "" {
					nullableType = "rasql.Nullable[" + mapping.GoType + "]"
				}
				tables[i].Columns[j].GoBinding = &schema.GoBinding{Type: mapping.GoType, NullableType: nullableType, Imports: importsFor(mapping.Imports)}
			}
		}
		for _, relation := range semanticByID[id].Relations {
			target := physicalByID[relation.Target]
			optionality := schema.RelationshipRequired
			if relation.Nullable {
				optionality = schema.RelationshipOptional
			}
			tables[i].Relationships = append(tables[i].Relationships, schema.RelationshipDef{Name: relation.Name, Kind: schema.RelationshipKind(relation.Kind), Optionality: optionality, Columns: slices.Clone(relation.From), ReferencedSchema: target.Schema, ReferencedTable: target.Name, ReferencedColumns: slices.Clone(relation.To)})
		}
	}
	resolved, err := schemagen.ResolveNames(in.Generation.Package, tables, toNameOverrides(names))
	if err != nil {
		return Store{}, err
	}
	for _, cfg := range in.Generation.Objects {
		physical := in.Catalog.Objects[0]
		for _, candidate := range in.Catalog.Objects {
			if candidate.ID == cfg.ID {
				physical = candidate
				break
			}
		}
		var table schema.TableDef
		for _, candidate := range tables {
			if candidate.Name == physical.Name && candidate.Schema == physical.Schema {
				table = candidate
				break
			}
		}
		object, ok := resolved.Object(table)
		if !ok {
			return Store{}, fmt.Errorf("generate: emitter object %q has no resolved names", cfg.ID)
		}
		if cfg.Source != "" && cfg.Source != object.Accessor || cfg.Row != "" && cfg.Row != object.RowType || cfg.File != "" && cfg.File != resolved.Filename(table) {
			return Store{}, fmt.Errorf("generate: unrepresentable legacy generation name for %q", cfg.ID)
		}
		if cfg.Create != "" || cfg.Patch != "" {
			wantCreate, wantPatch := resolved.MutationTypeNames(table)
			if cfg.Create != wantCreate || cfg.Patch != wantPatch {
				return Store{}, fmt.Errorf("generate: unrepresentable legacy mutation names for %q", cfg.ID)
			}
		}
	}
	return Store{Package: in.Generation.Package, Root: "", Dir: in.Generation.Output, Tables: tables, Names: names, Prune: in.Generation.Prune, Dialect: generationDialect(in.Catalog.Engine.Dialect)}, nil
}

func mappingFor(scalar string, mappings []compilerir.ScalarMapping) (compilerir.ScalarMapping, bool) {
	for _, mapping := range mappings {
		if mapping.Name == scalar {
			return mapping, true
		}
	}
	return compilerir.ScalarMapping{}, false
}
func importsFor(imports []compilerir.GoImport) []schema.GoImport {
	out := make([]schema.GoImport, len(imports))
	for i, imp := range imports {
		out[i] = schema.GoImport{Path: imp.Path, Name: imp.Alias}
	}
	return out
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
