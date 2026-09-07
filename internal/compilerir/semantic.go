package compilerir

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
}
type NativeMatch struct{ Dialect, Schema, Name, Kind, LogicalKind string }
type MappingConfig struct{ Scalars []ScalarMapping }
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
	for _, object := range c.Objects {
		so := SemanticObject{ID: object.ID, Kind: object.Kind, PhysicalName: QualifiedName{Schema: object.Schema, Name: object.Name}}
		for _, column := range object.Columns {
			scalar, found := scalarFor(column, mappings)
			if !found && column.LogicalKind == "native" {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "opaque_type", Path: object.Name + "." + column.Name, Message: "opaque native type requires an explicit mapping"})
			}
			if !found {
				scalar = column.LogicalKind
			}
			state := "optional"
			if column.GeneratedSQL != "" || column.Identity != "" {
				state = "generated"
			}
			if !column.Nullable && state == "optional" {
				state = "required"
			}
			so.Columns = append(so.Columns, SemanticColumn{Name: column.Name, Scalar: scalar, Nullable: column.Nullable, Readable: !column.Hidden, InsertState: state, PatchState: "settable", Certainty: certaintyFor(column)})
		}
		model.Objects = append(model.Objects, so)
	}
	for _, query := range queries {
		model.Queries = append(model.Queries, SemanticQuery{ID: query.ID, Name: query.Name, Parameters: cloneValues(query.Parameters), Results: cloneValues(query.Results), Cardinality: query.Cardinality})
		model.Diagnostics = append(model.Diagnostics, query.Diagnostics...)
	}
	model.Diagnostics = sortDiagnostics(model.Diagnostics)
	return model, model.Diagnostics
}

func scalarFor(column PhysicalColumn, config MappingConfig) (string, bool) {
	for _, mapping := range config.Scalars {
		m := mapping.Match
		if m.Dialect != "" && m.Dialect != column.Native.Dialect {
			continue
		}
		if m.Schema != "" && m.Schema != column.Native.Schema {
			continue
		}
		if m.Name != "" && m.Name != column.Native.Name {
			continue
		}
		if m.Kind != "" && m.Kind != column.Native.Kind {
			continue
		}
		if m.LogicalKind != "" && m.LogicalKind != column.LogicalKind {
			continue
		}
		return mapping.Name, true
	}
	return "", false
}
func certaintyFor(c PhysicalColumn) Certainty {
	if c.Native.Name == "" {
		return CertaintyUnknown
	}
	return CertaintyKnown
}
func cloneValues(values []SemanticValue) []SemanticValue {
	return append([]SemanticValue(nil), values...)
}

func (m SemanticModel) Validate() error { return ValidateSemantic(m) }
