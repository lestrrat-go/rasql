package compilerir

import "slices"

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
	if err := ValidatePhysical(c); err != nil {
		model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_physical", Path: "physical", Message: err.Error()})
	}
	objectIDs := map[QualifiedName]ObjectID{}
	for _, object := range c.Objects {
		objectIDs[QualifiedName{Schema: object.Schema, Name: object.Name}] = object.ID
	}
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
			if !found && scalar == "" {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "scalar_missing", Path: object.Name + "." + column.Name, Message: "logical scalar is missing"})
			}
			state := "optional"
			if column.GeneratedSQL != "" || column.Identity != "" {
				state = "generated"
			}
			if column.DefaultSQL != "" && state == "optional" {
				state = "optional"
			}
			if !column.Nullable && state == "optional" && column.DefaultSQL == "" {
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
			relation := SemanticRelation{Name: constraint.Name, Kind: "belongs_to", Target: objectIDs[QualifiedName{Schema: constraint.Reference.Schema, Name: constraint.Reference.Object}], From: append([]string(nil), constraint.Columns...), To: append([]string(nil), constraint.Reference.Columns...), Nullable: false}
			for _, column := range object.Columns {
				for _, from := range constraint.Columns {
					if column.Name == from && column.Nullable {
						relation.Nullable = true
					}
				}
			}
			so.Relations = append(so.Relations, relation)
		}
		model.Objects = append(model.Objects, so)
	}
	for _, query := range queries {
		model.Queries = append(model.Queries, SemanticQuery{ID: query.ID, Name: query.Name, Parameters: cloneValues(query.Parameters), Results: cloneValues(query.Results), Cardinality: query.Cardinality})
		model.Diagnostics = append(model.Diagnostics, query.Diagnostics...)
	}
	model.Diagnostics = sortDiagnostics(model.Diagnostics)
	return model, append([]Diagnostic(nil), model.Diagnostics...)
}

func scalarFor(column PhysicalColumn, config MappingConfig) (string, bool) {
	bestRank := -1
	best := ""
	matches := 0
	for _, mapping := range config.Scalars {
		m := mapping.Match
		nativeDialect, nativeSchema, nativeName, nativeKind := "", "", "", ""
		if column.Native != nil {
			nativeDialect, nativeSchema, nativeName, nativeKind = column.Native.Dialect, column.Native.Schema, column.Native.Name, column.Native.Kind
		}
		if m.Dialect != "" && m.Dialect != nativeDialect {
			continue
		}
		if m.Schema != "" && m.Schema != nativeSchema {
			continue
		}
		if m.Name != "" && m.Name != nativeName {
			continue
		}
		if m.Kind != "" && m.Kind != nativeKind {
			continue
		}
		if m.LogicalKind != "" && m.LogicalKind != column.LogicalKind {
			continue
		}
		rank := 0
		if m.LogicalKind != "" {
			rank = 1
		}
		if m.Name != "" {
			rank = 2
		}
		if m.Schema != "" && m.Name != "" {
			rank = 3
		}
		if rank > bestRank {
			bestRank, best, matches = rank, mapping.Name, 1
		} else if rank == bestRank {
			matches++
		}
	}
	return best, bestRank >= 0 && matches == 1
}
func certaintyFor(c PhysicalColumn) Certainty {
	if c.Native == nil || c.Native.Name == "" {
		return CertaintyUnknown
	}
	return CertaintyKnown
}
func cloneValues(values []SemanticValue) []SemanticValue {
	return slices.Clone(values)
}

func (m SemanticModel) Validate() error { return ValidateSemantic(m) }
