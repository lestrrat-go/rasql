package compilerir

type GoModel struct {
	Package string
	Imports []GoImport
	Files   []GoFile
	Objects []GoObject
	Queries []GoQuery
}
type GoFile struct {
	Path         string
	Declarations []string
}
type GoImport struct{ Path, Alias string }
type GoField struct {
	Name, Type, Codec string
	Nullable          bool
}
type GoShape struct {
	Name        string
	Fields      []GoField
	DecoderName string
}
type GoObject struct {
	ID            ObjectID
	SourceName    string
	Row           GoShape
	Create, Patch *GoShape
	Columns       []GoColumn
	Relations     []GoRelation
}
type GoColumn struct {
	Name, PhysicalName, Scalar, GoType, Codec string
	Nullable                                  bool
}
type GoRelation struct {
	Name     string
	Target   ObjectID
	Kind     string
	Nullable bool
}
type GoQuery struct {
	ID                          QueryID
	Name                        string
	Parameters                  []GoField
	Result                      *GoShape
	ProjectionName, Cardinality string
}
type ObjectGoName struct {
	ID                               ObjectID
	Source, Row, Create, Patch, File string
}
type QueryGoName struct {
	ID                                          QueryID
	Function, Result, Projection, Decoder, File string
}
type GoConfig struct {
	Package, Output string
	Objects         []ObjectGoName
	Queries         []QueryGoName
	Scalars         []ScalarMapping
	Emitter         string
	Prune           bool
}

func BuildGo(model SemanticModel, config GoConfig) (GoModel, []Diagnostic) {
	out := GoModel{Package: config.Package}
	var diagnostics []Diagnostic
	if err := ValidateSemantic(model); err != nil {
		diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_semantic", Path: "semantic", Message: err.Error()})
	}
	if config.Emitter != "" && config.Emitter != "compact" && config.Emitter != "legacy" {
		diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_emitter", Path: "emitter", Message: "emitter must be compact or legacy"})
	}
	for _, object := range model.Objects {
		name := object.PhysicalName.Name + "Row"
		for _, configured := range config.Objects {
			if configured.ID == object.ID {
				if configured.Row != "" {
					name = configured.Row
				}
				if configured.File != "" {
					out.Files = append(out.Files, GoFile{Path: configured.File})
				}
			}
		}
		goObject := GoObject{ID: object.ID, SourceName: object.PhysicalName.Name, Row: GoShape{Name: name}}
		for _, configured := range config.Objects {
			if configured.ID == object.ID {
				if configured.Create != "" {
					goObject.Create = &GoShape{Name: configured.Create}
				}
				if configured.Patch != "" {
					goObject.Patch = &GoShape{Name: configured.Patch}
				}
			}
		}
		for _, column := range object.Columns {
			if !knownScalar(column.Scalar) {
				diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unsupported_scalar", Path: object.PhysicalName.Name + "." + column.Name, Message: "scalar has no built-in Go mapping"})
			}
			goColumn := GoColumn{Name: column.Name, PhysicalName: column.Name, Scalar: column.Scalar, GoType: goType(column.Scalar, column.Nullable), Nullable: column.Nullable}
			goObject.Columns = append(goObject.Columns, goColumn)
			goObject.Row.Fields = append(goObject.Row.Fields, GoField{Name: column.Name, Type: goColumn.GoType, Nullable: column.Nullable})
			if goObject.Create != nil && column.InsertState != "forbidden" && column.InsertState != "generated" {
				goObject.Create.Fields = append(goObject.Create.Fields, GoField{Name: column.Name, Type: goColumn.GoType, Nullable: column.Nullable})
			}
			if goObject.Patch != nil && column.PatchState != "forbidden" {
				goObject.Patch.Fields = append(goObject.Patch.Fields, GoField{Name: column.Name, Type: goColumn.GoType, Nullable: column.Nullable})
			}
		}
		for _, relation := range object.Relations {
			goObject.Relations = append(goObject.Relations, GoRelation{Name: relation.Name, Target: relation.Target, Kind: relation.Kind, Nullable: relation.Nullable})
		}
		out.Objects = append(out.Objects, goObject)
	}
	for _, query := range model.Queries {
		goQuery := GoQuery{ID: query.ID, Name: query.Name, Cardinality: query.Cardinality}
		for _, value := range query.Parameters {
			goQuery.Parameters = append(goQuery.Parameters, GoField{Name: value.Name, Type: goType(value.Scalar, value.Nullable), Nullable: value.Nullable})
		}
		if len(query.Results) > 0 {
			shape := &GoShape{Name: query.Name + "Result"}
			for _, value := range query.Results {
				shape.Fields = append(shape.Fields, GoField{Name: value.Name, Type: goType(value.Scalar, value.Nullable), Nullable: value.Nullable})
			}
			goQuery.Result = shape
		}
		out.Queries = append(out.Queries, goQuery)
	}
	diagnostics = append(diagnostics, model.Diagnostics...)
	diagnostics = sortDiagnostics(diagnostics)
	return out, diagnostics
}
func knownScalar(s string) bool {
	switch s {
	case "boolean", "integer", "float", "text", "bytes", "time", "json", "uuid", "decimal":
		return true
	default:
		return false
	}
}
func goType(scalar string, nullable bool) string {
	base := map[string]string{"boolean": "bool", "integer": "int64", "float": "float64", "text": "string", "bytes": "[]byte", "time": "time.Time", "json": "[]byte", "uuid": "string", "decimal": "string"}[scalar]
	if base == "" {
		base = scalar
	}
	if nullable {
		return "rasql.Nullable[" + base + "]"
	}
	return base
}
func (m GoModel) Validate() error { return ValidateGo(m) }
