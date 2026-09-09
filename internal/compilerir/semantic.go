package compilerir

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
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

type relationCandidate struct {
	source, target               ObjectID
	sourceName, targetName       QualifiedName
	from, to                     []string
	nullable                     bool
	directName, physicalIdentity string
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
	semanticIndexes := make(map[ObjectID]int, len(c.Objects))
	relationNames := make(map[ObjectID]map[string]struct{}, len(c.Objects))
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
		semanticIndexes[object.ID] = len(model.Objects)
		relationNames[object.ID] = make(map[string]struct{}, len(object.Constraints)+len(mappings.Relations))
		model.Objects = append(model.Objects, so)
	}
	candidates := relationCandidates(c, objectIDs, objectColumns, &model)
	pairCounts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		pairCounts[relationPairKey(candidate.source, candidate.target)]++
	}
	for _, candidate := range candidates {
		appendSemanticRelation(&model, semanticIndexes, relationNames, candidate.source, SemanticRelation{
			Name: candidate.directName, Kind: "belongs_to", Target: candidate.target,
			From: append([]string(nil), candidate.from...), To: append([]string(nil), candidate.to...), Nullable: candidate.nullable,
		}, relationPath(candidate.sourceName, candidate.directName))
	}
	for _, candidate := range candidates {
		if _, ok := semanticIndexes[candidate.target]; !ok {
			continue
		}
		inverseName := RelationGoName(candidate.sourceName.Name)
		if pairCounts[relationPairKey(candidate.source, candidate.target)] > 1 {
			inverseName = candidate.directName + RelationGoName(candidate.sourceName.Name)
		}
		kind := "has_many"
		if child, ok := physicalObjectByID(c.Objects, candidate.source); ok && completeUniqueConstraint(child, candidate.from) {
			kind = "has_one"
		}
		appendSemanticRelation(&model, semanticIndexes, relationNames, candidate.target, SemanticRelation{
			Name: inverseName, Kind: kind, Target: candidate.source,
			From: append([]string(nil), candidate.to...), To: append([]string(nil), candidate.from...),
		}, relationPath(candidate.targetName, inverseName))
	}
	for mappingIndex, mapping := range mappings.Relations {
		index, ok := semanticIndexes[mapping.Source]
		if !ok {
			continue
		}
		if _, exists := relationNames[mapping.Source][RelationGoName(mapping.Name)]; exists {
			model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "relation_name_collision", Path: fmt.Sprintf("mappings.relations[%d].name", mappingIndex), Message: "relation name collides with another relation on the source object"})
		} else {
			relationNames[mapping.Source][RelationGoName(mapping.Name)] = struct{}{}
		}
		through := mapping.Through
		model.Objects[index].Relations = append(model.Objects[index].Relations, SemanticRelation{Name: mapping.Name, Kind: "many_through", From: append([]string(nil), mapping.From...), Target: mapping.Target, To: append([]string(nil), mapping.To...), Through: &SemanticThrough{Object: through.Object, SourceFrom: append([]string(nil), through.SourceFrom...), SourceTo: append([]string(nil), through.SourceTo...), TargetFrom: append([]string(nil), through.TargetFrom...), TargetTo: append([]string(nil), through.TargetTo...)}})
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

func relationCandidates(c PhysicalCatalog, objectIDs map[QualifiedName]ObjectID, objectColumns map[QualifiedName]map[string]struct{}, model *SemanticModel) []relationCandidate {
	var candidates []relationCandidate
	for _, object := range c.Objects {
		for constraintIndex, constraint := range object.Constraints {
			if constraint.Kind != "foreign_key" || constraint.Reference == nil {
				continue
			}
			q, _, resolved := resolveForeignReference(c.Engine, object.Schema, *constraint.Reference, objectColumns)
			path := fmt.Sprintf("%s.constraints[%d]", object.Name, constraintIndex)
			if !resolved {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "ambiguous_relation_target", Path: path, Message: "foreign key target is unresolved or ambiguous"})
			}
			target, ok := objectIDs[q]
			if !ok {
				model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "unresolved_relation_target", Path: path, Message: "foreign key target does not exist"})
			}
			nullable := false
			for _, column := range object.Columns {
				if column.Nullable && slices.Contains(constraint.Columns, column.Name) {
					nullable = true
					break
				}
			}
			candidates = append(candidates, relationCandidate{
				source: object.ID, target: target,
				sourceName: QualifiedName{Schema: object.Schema, Name: object.Name}, targetName: q,
				from: append([]string(nil), constraint.Columns...), to: append([]string(nil), constraint.Reference.Columns...),
				nullable:         nullable,
				directName:       relationName(object.Name, constraint.Columns, q.Name),
				physicalIdentity: relationPhysicalIdentity(object, constraint, constraintIndex, q),
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].physicalIdentity < candidates[j].physicalIdentity })
	return candidates
}

func appendSemanticRelation(model *SemanticModel, indexes map[ObjectID]int, names map[ObjectID]map[string]struct{}, source ObjectID, relation SemanticRelation, path string) {
	index, ok := indexes[source]
	if !ok {
		return
	}
	if _, exists := names[source][RelationGoName(relation.Name)]; exists {
		model.Diagnostics = append(model.Diagnostics, Diagnostic{Level: DiagnosticError, Code: "relation_name_collision", Path: path, Message: "relation name is duplicated"})
	} else {
		names[source][RelationGoName(relation.Name)] = struct{}{}
	}
	model.Objects[index].Relations = append(model.Objects[index].Relations, relation)
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
		if !hasColumns(source, mapping.From) || !hasColumns(target, mapping.To) || !hasColumns(through, mapping.Through.SourceFrom) || !hasColumns(through, mapping.Through.TargetFrom) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unresolved_mapping_column", Path: path, Message: "mapping references an unknown column"})
		}
		if !hasForeignKey(through, mapping.Through.SourceFrom, source, mapping.Through.SourceTo) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "missing_mapping_foreign_key", Path: path, Message: "through source path does not reference the source object"})
		}
		if !hasForeignKey(through, mapping.Through.TargetFrom, target, mapping.Through.TargetTo) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "missing_mapping_foreign_key", Path: path, Message: "through target path does not reference the target object"})
		}
		if !compatiblePath(through, mapping.Through.SourceFrom, source, mapping.Through.SourceTo) || !compatiblePath(through, mapping.Through.TargetFrom, target, mapping.Through.TargetTo) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "mapping_type_mismatch", Path: path, Message: "foreign key path logical types do not match"})
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

func compatiblePath(left PhysicalObject, leftNames []string, right PhysicalObject, rightNames []string) bool {
	if len(leftNames) != len(rightNames) {
		return false
	}
	for i := range leftNames {
		var leftColumn, rightColumn *PhysicalColumn
		for j := range left.Columns {
			if left.Columns[j].Name == leftNames[i] {
				leftColumn = &left.Columns[j]
			}
		}
		for j := range right.Columns {
			if right.Columns[j].Name == rightNames[i] {
				rightColumn = &right.Columns[j]
			}
		}
		if leftColumn == nil || rightColumn == nil || leftColumn.LogicalKind != rightColumn.LogicalKind {
			return false
		}
	}
	return true
}

func scalarFor(column PhysicalColumn, config MappingConfig) (string, bool, bool) {
	selection := selectMapping(column, config)
	return selection.scalar, selection.found, selection.ambiguous
}
func relationName(source string, columns []string, target string) string {
	name := ""
	if len(columns) > 0 {
		name = strings.TrimSuffix(columns[0], "_id")
	}
	name = RelationGoName(name)
	if name == "" {
		name = RelationGoName(target)
	}
	if name == "" {
		return RelationGoName(source)
	}
	return name
}

// RelationGoName returns the canonical Go identifier used for a relation's
// generated method and factory name. Callers retain the original relation
// label for runtime diagnostics and graph metadata.
func RelationGoName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' })
	var result strings.Builder
	for _, part := range parts {
		switch strings.ToLower(part) {
		case "api":
			result.WriteString("API")
		case "id":
			result.WriteString("ID")
		case "json":
			result.WriteString("JSON")
		case "url":
			result.WriteString("URL")
		case "uuid":
			result.WriteString("UUID")
		default:
			for index, r := range part {
				if index == 0 {
					result.WriteRune(unicode.ToUpper(r))
					continue
				}
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}
func relationPhysicalIdentity(object PhysicalObject, constraint PhysicalConstraint, index int, target QualifiedName) string {
	return strings.Join([]string{
		object.Schema, object.Name, target.Schema, target.Name,
		strings.Join(constraint.Columns, "\x00"), strings.Join(constraint.Reference.Columns, "\x00"),
		constraint.Name, fmt.Sprintf("%08d", index),
	}, "\x00")
}
func relationPairKey(source, target ObjectID) string { return string(source) + "\x00" + string(target) }
func relationPath(object QualifiedName, name string) string {
	return object.Name + ".relations." + name
}
func physicalObjectByID(objects []PhysicalObject, id ObjectID) (PhysicalObject, bool) {
	for _, object := range objects {
		if object.ID == id {
			return object, true
		}
	}
	return PhysicalObject{}, false
}
func completeUniqueConstraint(object PhysicalObject, columns []string) bool {
	for _, constraint := range object.Constraints {
		if (constraint.Kind == "primary_key" || constraint.Kind == "unique") && slices.Equal(constraint.Columns, columns) {
			return true
		}
	}
	return false
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
