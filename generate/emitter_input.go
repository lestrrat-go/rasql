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
	Mappings   compilerir.MappingConfig
	Generation compilerir.GoConfig
}

func NewEmitterInput(catalog compilerir.PhysicalCatalog, semantic compilerir.SemanticModel, goModel compilerir.GoModel, generation compilerir.GoConfig, mapping ...compilerir.MappingConfig) (EmitterInput, error) {
	if len(mapping) > 1 {
		return EmitterInput{}, fmt.Errorf("generate: emitter mappings may be supplied only once")
	}
	var mappings compilerir.MappingConfig
	if len(mapping) == 1 {
		mappings = mapping[0]
	}
	in := EmitterInput{Catalog: catalog.Clone(), Semantic: semantic.Clone(), Go: goModel.Clone(), Mappings: mappings.Clone(), Generation: generation.Clone()}
	if err := in.Validate(); err != nil {
		return EmitterInput{}, err
	}
	return in, nil
}

func (in EmitterInput) Clone() EmitterInput {
	return EmitterInput{Catalog: in.Catalog.Clone(), Semantic: in.Semantic.Clone(), Go: in.Go.Clone(), Mappings: in.Mappings.Clone(), Generation: in.Generation.Clone()}
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
	if in.Generation.Emitter != "legacy" && in.Generation.Emitter != "compact" {
		return fmt.Errorf("generate: emitter generation.emitter must be compact or legacy")
	}
	if in.Generation.Package == "" || in.Generation.Output == "" {
		return fmt.Errorf("generate: emitter generation package and output are required")
	}
	if err := compilerir.ValidateMappingConfig(in.Mappings, in.Generation.Package); err != nil {
		return fmt.Errorf("generate: emitter generation mappings: %w", err)
	}
	if !reflect.DeepEqual(in.Generation.Scalars, in.Mappings.Scalars) {
		return fmt.Errorf("generate: emitter generation scalar mappings disagree with canonical mappings")
	}
	if in.Go.Package != in.Generation.Package {
		return fmt.Errorf("generate: emitter Go package %q disagrees with generation package %q", in.Go.Package, in.Generation.Package)
	}
	canonicalSemantic, diagnostics := compilerir.BuildSemantic(in.Catalog, in.Mappings, nil)
	if len(diagnostics) != 0 {
		return fmt.Errorf("generate: emitter canonical semantic diagnostics: %v", diagnostics)
	}
	if !canonicalSemanticMatches(in.Semantic, canonicalSemantic) {
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
		if err := validateGoObject(object, p, s, configured[object.ID], in.Mappings.Scalars); err != nil {
			return err
		}
		goObjects[object.ID] = struct{}{}
	}
	if len(goObjects) != len(physical) {
		return fmt.Errorf("generate: emitter Go model does not cover every catalog object")
	}
	if err := validateQueryModel(in.Semantic.Queries, in.Go.Queries, in.Generation.Queries); err != nil {
		return err
	}
	canonicalGo, diagnostics := compilerir.BuildGo(in.Semantic, in.Generation)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return fmt.Errorf("generate: emitter canonical Go diagnostics: %s", diagnostic.Message)
		}
	}
	if !reflect.DeepEqual(normalizeViewShapes(in.Go, in.Catalog), normalizeViewShapes(canonicalGo, in.Catalog)) {
		return fmt.Errorf("generate: emitter Go model disagrees with canonical semantic and generation policy")
	}
	return validateGenerationFiles(in.Go, in.Generation, physical)
}

func canonicalSemanticMatches(got, want compilerir.SemanticModel) bool {
	if len(got.Objects) != len(want.Objects) {
		return false
	}
	for index, object := range got.Objects {
		canonical := want.Objects[index]
		if object.ID != canonical.ID || object.Kind != canonical.Kind || object.PhysicalName != canonical.PhysicalName || len(object.Columns) != len(canonical.Columns) || len(object.Relations) != len(canonical.Relations) {
			return false
		}
		for columnIndex, column := range object.Columns {
			other := canonical.Columns[columnIndex]
			if column != other {
				return false
			}
		}
		for relationIndex, relation := range object.Relations {
			other := canonical.Relations[relationIndex]
			if relation.Name != other.Name || relation.Kind != other.Kind || relation.Target != other.Target || relation.Nullable != other.Nullable || !reflect.DeepEqual(relation.From, other.From) || !reflect.DeepEqual(relation.To, other.To) || !reflect.DeepEqual(relation.Through, other.Through) {
				return false
			}
		}
	}
	return true
}

func validateQueryModel(semantic []compilerir.SemanticQuery, model []compilerir.GoQuery, configured []compilerir.QueryGoName) error {
	goByID := make(map[compilerir.QueryID]compilerir.GoQuery, len(model))
	for _, query := range model {
		goByID[query.ID] = query
	}
	cfgByID := make(map[compilerir.QueryID]compilerir.QueryGoName, len(configured))
	for _, query := range configured {
		cfgByID[query.ID] = query
	}
	for _, query := range semantic {
		goQuery, ok := goByID[query.ID]
		if !ok || goQuery.Cardinality != query.Cardinality || goQuery.Name == "" {
			return fmt.Errorf("generate: emitter query %q disagrees with semantic model", query.ID)
		}
		if query.Cardinality == "exec" && goQuery.Result != nil || query.Cardinality != "exec" && goQuery.Result == nil {
			return fmt.Errorf("generate: emitter query %q result shape disagrees with cardinality", query.ID)
		}
		if cfg, ok := cfgByID[query.ID]; ok {
			if cfg.Function != "" && cfg.Function != goQuery.Name || cfg.Result != "" && goQuery.Result != nil && cfg.Result != goQuery.Result.Name || cfg.Decoder != "" && goQuery.Result != nil && cfg.Decoder != goQuery.Result.DecoderName {
				return fmt.Errorf("generate: emitter query %q disagrees with generation names", query.ID)
			}
		}
	}
	if len(goByID) != len(semantic) {
		return fmt.Errorf("generate: emitter Go model query coverage disagrees with semantic model")
	}
	return nil
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
		if !reflect.DeepEqual(relation.From, want.From) || !reflect.DeepEqual(relation.To, want.To) || !goThroughMatchesSemantic(relation.Through, want.Through) {
			return fmt.Errorf("generate: emitter Go object %q relation %d path disagrees with semantic model", object.ID, i)
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

func goThroughMatchesSemantic(got *compilerir.GoThrough, want *compilerir.SemanticThrough) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Object == want.Object && reflect.DeepEqual(got.SourceFrom, want.SourceFrom) && reflect.DeepEqual(got.SourceTo, want.SourceTo) && reflect.DeepEqual(got.TargetFrom, want.TargetFrom) && reflect.DeepEqual(got.TargetTo, want.TargetTo)
}

func validateGoModel(model compilerir.GoModel, catalog compilerir.PhysicalCatalog) error {
	copy := normalizeViewShapes(model, catalog)
	return compilerir.ValidateGo(copy)
}

func normalizeViewShapes(model compilerir.GoModel, catalog compilerir.PhysicalCatalog) compilerir.GoModel {
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
	return copy
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
	for _, cfg := range generation.Queries {
		if cfg.File == "" {
			continue
		}
		if _, ok := files[cfg.File]; !ok {
			return fmt.Errorf("generate: emitter query %q file %q is absent from Go model", cfg.ID, cfg.File)
		}
	}
	return nil
}

// legacyStore adapts canonical facts for retained internal comparisons.
func legacyStore(in EmitterInput) (Store, error) {
	if err := in.Validate(); err != nil {
		return Store{}, err
	}
	if in.Generation.Emitter != "legacy" {
		return Store{}, fmt.Errorf("generate: legacy renderer requires generation.emitter legacy")
	}
	tables, diagnostics := compilerir.TableDefsFromPhysical(in.Catalog)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Store{}, fmt.Errorf("generate: emitter: %s", diagnostic.Message)
		}
	}
	for _, mapping := range in.Mappings.Scalars {
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
	names := make(map[schema.ObjectName]legacyObjectNames, len(tables))
	configByID := make(map[compilerir.ObjectID]compilerir.ObjectGoName, len(in.Generation.Objects))
	for _, cfg := range in.Generation.Objects {
		configByID[cfg.ID] = cfg
	}
	for i := range tables {
		id := in.Catalog.Objects[i].ID
		goObject := goByID[id]
		cfg := configByID[id]
		names[tables[i].ObjectName()] = legacyObjectNames{Accessor: cfg.Source, RowType: cfg.Row}
		if cfg.File != "" {
			names[tables[i].ObjectName()] = legacyObjectNames{Accessor: cfg.Source, RowType: cfg.Row, FileBase: strings.TrimSuffix(cfg.File, "_gen.go")}
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
	return Store{
		Package: in.Generation.Package, Root: "", Dir: in.Generation.Output, Prune: in.Generation.Prune,
		legacyTables: tables, legacyNames: names, legacyDialect: generationDialect(in.Catalog.Engine.Dialect),
	}, nil
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
