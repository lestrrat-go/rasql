package compilerir

import (
	"fmt"
	"slices"
)

type SemanticModel struct {
	Objects     []SemanticObject
	Queries     []SemanticQuery
	Diagnostics []Diagnostic
}
type SemanticObject struct {
	ID           ObjectID
	Kind         string
	PhysicalName QualifiedName
	Columns      []SemanticColumn
	Relations    []SemanticRelation
}
type QualifiedName struct{ Schema, Name string }
type SemanticColumn struct {
	Name, Scalar            string
	Nullable, Readable      bool
	InsertState, PatchState string
	Certainty               Certainty
}
type SemanticRelation struct {
	Name, Kind string
	From       []string
	Target     ObjectID
	To         []string
	Nullable   bool
	Through    *SemanticThrough
}
type SemanticThrough struct {
	Object                                     ObjectID
	SourceFrom, SourceTo, TargetFrom, TargetTo []string
}
type SemanticQuery struct {
	ID          QueryID
	Name        string
	Parameters  []SemanticValue
	Results     []SemanticValue
	Cardinality string
}
type SemanticValue struct {
	Name, Scalar                        string
	Nullable                            bool
	TypeCertainty, NullabilityCertainty Certainty
	LogicalKind                         string
	Native                              *NativeType
	Integer                             *IntegerTypeFacts
}
type NativeMatch struct{ Dialect, Schema, Name, Kind, LogicalKind string }
type MappingConfig struct {
	Scalars   []ScalarMapping
	Relations []RelationMapping
}
type RelationMapping struct {
	Name    string
	Source  ObjectID
	From    []string
	Target  ObjectID
	To      []string
	Through ThroughMapping
}
type ThroughMapping struct {
	Object                                     ObjectID
	SourceFrom, SourceTo, TargetFrom, TargetTo []string
}
type ScalarMapping struct {
	Name                   string
	Match                  NativeMatch
	GoType, NullableGoType string
	Imports                []GoImport
	Codec                  string
}
type QueryAnalysis struct {
	ID                       QueryID
	Name, SQLPath, SQLSHA256 string
	Engine                   EngineIdentity
	Operation                string
	Parameters, Results      []SemanticValue
	Cardinality              string
	Diagnostics              []Diagnostic
}

func BuildSemantic(c PhysicalCatalog, mappings MappingConfig, queries []QueryAnalysis) (SemanticModel, []Diagnostic) {
	model := SemanticModel{}
	if err := ValidateMappingConfig(MappingConfig{Relations: mappings.Relations}, ""); err != nil {
		model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_mapping", Path: "mappings", Message: err.Error()})
	}
	if err := ValidatePhysical(c); err != nil {
		model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_physical", Path: "physical", Message: err.Error()})
	}
	objectIDs := map[QualifiedName]ObjectID{}
	objectColumns := map[QualifiedName]map[string]struct{}{}
	for _, object := range c.Objects {
		q := QualifiedName{Schema: object.Schema, Name: object.Name}
		objectIDs[q] = object.ID
		columns := map[string]struct{}{}
		for _, column := range object.Columns {
			columns[column.Name] = struct{}{}
		}
		objectColumns[q] = columns
	}
	for _, object := range c.Objects {
		so := SemanticObject{ID: object.ID, Kind: object.Kind, PhysicalName: QualifiedName{Schema: object.Schema, Name: object.Name}}
		for _, column := range object.Columns {
			scalar, found, ambiguous := scalarFor(column, mappings)
			if ambiguous {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "ambiguous_scalar", Path: object.Name + "." + column.Name, Message: "multiple scalar mappings have the same precedence"})
			}
			if !found && column.LogicalKind == "native" {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "opaque_type", Path: object.Name + "." + column.Name, Message: "opaque native type requires an explicit mapping"})
			}
			if !found {
				scalar = column.LogicalKind
			}
			if !found && scalar == "" {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "scalar_missing", Path: object.Name + "." + column.Name, Message: "logical scalar is missing"})
			}
			state := "optional"
			if column.GeneratedSQL != "" || column.Identity == "ALWAYS" {
				state = "generated"
			}
			if column.Identity == "BY DEFAULT" {
				state = "optional"
			}
			if column.DefaultSQL != "" && state == "optional" {
				state = "optional"
			}
			if !column.Nullable && state == "optional" && column.DefaultSQL == "" && column.Identity != "BY DEFAULT" {
				state = "required"
			}
			patchState := "settable"
			if state == "generated" {
				patchState = "forbidden"
			}
			if object.Kind == "view" {
				state, patchState = "forbidden", "forbidden"
			}
			so.Columns = append(so.Columns, SemanticColumn{Name: column.Name, Scalar: scalar, Nullable: column.Nullable, Readable: !column.Hidden, InsertState: state, PatchState: patchState, Certainty: certaintyFor(column)})
		}
		for _, constraint := range object.Constraints {
			if constraint.Kind != "foreign_key" || constraint.Reference == nil {
				continue
			}
			q, _, resolved := resolveForeignReference(c.Engine, object.Schema, *constraint.Reference, objectColumns)
			if !resolved {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "ambiguous_relation_target", Path: object.Name + "." + constraint.Name, Message: "foreign key target is unresolved or ambiguous"})
			}
			if _, ok := objectIDs[q]; !ok {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "unresolved_relation_target", Path: object.Name + "." + constraint.Name, Message: "foreign key target does not exist"})
			}
			name := constraint.Name
			if name == "" {
				name = relationName(object.Name, constraint.Columns, q.Name)
			}
			relation := SemanticRelation{Name: name, Kind: "belongs_to", Target: objectIDs[q], From: append([]string(nil), constraint.Columns...), To: append([]string(nil), constraint.Reference.Columns...), Nullable: false}
			for _, column := range object.Columns {
				for _, from := range constraint.Columns {
					if column.Name == from && column.Nullable {
						relation.Nullable = true
					}
				}
			}
			so.Relations = append(so.Relations, relation)
		}
		for _, mapping := range mappings.Relations {
			if mapping.Source != object.ID {
				continue
			}
			through := mapping.Through
			so.Relations = append(so.Relations, SemanticRelation{Name: mapping.Name, Kind: "many_through", From: append([]string(nil), mapping.From...), Target: mapping.Target, To: append([]string(nil), mapping.To...), Through: &SemanticThrough{Object: through.Object, SourceFrom: append([]string(nil), through.SourceFrom...), SourceTo: append([]string(nil), through.SourceTo...), TargetFrom: append([]string(nil), through.TargetFrom...), TargetTo: append([]string(nil), through.TargetTo...)}})
		}
		model.Objects = append(model.Objects, so)
	}
	model.Diagnostics = append(model.Diagnostics, validateRelationMappings(c, mappings)...)
	for _, query := range queries {
		model.Queries = append(model.Queries, SemanticQuery{ID: query.ID, Name: query.Name, Parameters: cloneValues(query.Parameters), Results: cloneValues(query.Results), Cardinality: query.Cardinality})
		model.Diagnostics = append(model.Diagnostics, query.Diagnostics...)
	}
	model.Diagnostics = sortDiagnostics(model.Diagnostics)
	if err := ValidateSemantic(model); err != nil {
		model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_semantic", Path: "semantic", Message: err.Error()})
		model.Diagnostics = sortDiagnostics(model.Diagnostics)
	}
	return model, append([]Diagnostic(nil), model.Diagnostics...)
}

func validateRelationMappings(c PhysicalCatalog, mappings MappingConfig) []Diagnostic {
	objects := make(map[ObjectID]PhysicalObject, len(c.Objects))
	for _, object := range c.Objects {
		objects[object.ID] = object
	}
	var diagnostics []Diagnostic
	for i, mapping := range mappings.Relations {
		path := fmt.Sprintf("mappings.relations[%d]", i)
		source, sourceOK := objects[mapping.Source]
		target, targetOK := objects[mapping.Target]
		through, throughOK := objects[mapping.Through.Object]
		if !sourceOK || !targetOK || !throughOK {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unresolved_mapping_object", Path: path, Message: "source, target, and through objects must exist"})
			continue
		}
		if source.Kind == "view" || target.Kind == "view" || through.Kind == "view" {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_mapping_object", Path: path, Message: "many-through mappings require physical tables"})
		}
		if !hasColumns(source, mapping.From) || !hasColumns(target, mapping.To) || !hasColumns(through, mapping.Through.SourceTo) || !hasColumns(through, mapping.Through.TargetTo) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unresolved_mapping_column", Path: path, Message: "mapping references an unknown column"})
		}
		if !hasForeignKey(through, mapping.Through.SourceTo, source, mapping.Through.SourceFrom) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "missing_mapping_foreign_key", Path: path, Message: "through source path does not reference the source object"})
		}
		if !hasForeignKey(through, mapping.Through.TargetTo, target, mapping.Through.TargetFrom) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "missing_mapping_foreign_key", Path: path, Message: "through target path does not reference the target object"})
		}
	}
	return diagnostics
}

func hasColumns(object PhysicalObject, names []string) bool {
	columns := make(map[string]struct{}, len(object.Columns))
	for _, column := range object.Columns {
		columns[column.Name] = struct{}{}
	}
	for _, name := range names {
		if _, ok := columns[name]; !ok {
			return false
		}
	}
	return true
}

func hasForeignKey(through PhysicalObject, columns []string, target PhysicalObject, targetColumns []string) bool {
	for _, constraint := range through.Constraints {
		if constraint.Kind != "foreign_key" || constraint.Reference == nil || !slices.Equal(constraint.Columns, columns) || !slices.Equal(constraint.Reference.Columns, targetColumns) {
			continue
		}
		if constraint.Reference.Object == target.Name && (constraint.Reference.Schema == "" || constraint.Reference.Schema == target.Schema) {
			return true
		}
	}
	return false
}

func scalarFor(column PhysicalColumn, config MappingConfig) (string, bool, bool) {
	selection := selectMapping(column, config)
	return selection.scalar, selection.found, selection.ambiguous
}
func relationName(source string, columns []string, target string) string {
	if len(columns) == 0 {
		return source + "To" + target
	}
	return source + "To" + target + "By" + columns[0]
}
func certaintyFor(c PhysicalColumn) Certainty {
	if c.Native == nil || c.Native.Name == "" {
		return CertaintyUnknown
	}
	return CertaintyKnown
}
func cloneValues(values []SemanticValue) []SemanticValue {
	out := slices.Clone(values)
	for i := range out {
		out[i].Native = cloneNative(out[i].Native)
		if values[i].Integer != nil {
			integer := *values[i].Integer
			out[i].Integer = &integer
		}
	}
	return out
}

func (m SemanticModel) Validate() error { return ValidateSemantic(m) }
