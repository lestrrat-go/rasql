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
	for _, mapping := range config.Scalars {
		if !knownScalar(mapping.Name) {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unsupported_scalar", Path: "scalars." + mapping.Name, Message: "custom scalar mappings are owned by G2"})
		}
		out.Imports = append(out.Imports, mapping.Imports...)
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
		sourceName := object.PhysicalName.Name
		createName, patchName := object.PhysicalName.Name+"Create", object.PhysicalName.Name+"Patch"
		for _, configured := range config.Objects {
			if configured.ID == object.ID {
				if configured.Source != "" {
					sourceName = configured.Source
				}
				if configured.Create != "" {
					createName = configured.Create
				}
				if configured.Patch != "" {
					patchName = configured.Patch
				}
			}
		}
		goObject := GoObject{ID: object.ID, SourceName: sourceName, Row: GoShape{Name: name, DecoderName: name + "Decoder"}, Create: &GoShape{Name: createName}, Patch: &GoShape{Name: patchName}}
		for _, column := range object.Columns {
			if !knownScalar(column.Scalar) {
				diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unsupported_scalar", Path: object.PhysicalName.Name + "." + column.Name, Message: "scalar has no built-in Go mapping"})
			}
			goColumn := GoColumn{Name: column.Name, PhysicalName: column.Name, Scalar: column.Scalar, GoType: goType(column.Scalar, column.Nullable), Nullable: column.Nullable}
			goObject.Columns = append(goObject.Columns, goColumn)
			if column.Readable {
				goObject.Row.Fields = append(goObject.Row.Fields, GoField{Name: column.Name, Type: goColumn.GoType, Nullable: column.Nullable})
			}
			if column.InsertState != "forbidden" && column.InsertState != "generated" {
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
		var configured QueryGoName
		for _, cfg := range config.Queries {
			if cfg.ID == query.ID {
				configured = cfg
				break
			}
		}
		if configured.Function != "" {
			goQuery.Name = configured.Function
		}
		goQuery.ProjectionName = configured.Projection
		if configured.File != "" {
			out.Files = append(out.Files, GoFile{Path: configured.File})
		}
		for _, value := range query.Parameters {
			if !knownScalar(value.Scalar) {
				diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unsupported_scalar", Path: query.Name + ".parameters." + value.Name, Message: "scalar has no built-in Go mapping"})
			}
			goQuery.Parameters = append(goQuery.Parameters, GoField{Name: value.Name, Type: goType(value.Scalar, value.Nullable), Nullable: value.Nullable})
		}
		if len(query.Results) > 0 {
			shape := &GoShape{Name: query.Name + "Result", DecoderName: query.Name + "ResultDecoder"}
			for _, value := range query.Results {
				if !knownScalar(value.Scalar) {
					diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "unsupported_scalar", Path: query.Name + ".results." + value.Name, Message: "scalar has no built-in Go mapping"})
				}
				shape.Fields = append(shape.Fields, GoField{Name: value.Name, Type: goType(value.Scalar, value.Nullable), Nullable: value.Nullable})
			}
			goQuery.Result = shape
		}
		if configured.Result != "" {
			if goQuery.Result == nil {
				goQuery.Result = &GoShape{Name: configured.Result, DecoderName: query.Name + "ResultDecoder"}
			}
			goQuery.Result.Name = configured.Result
		}
		if configured.Decoder != "" {
			if goQuery.Result == nil {
				goQuery.Result = &GoShape{Name: query.Name + "Result"}
			}
			goQuery.Result.DecoderName = configured.Decoder
		}
		out.Queries = append(out.Queries, goQuery)
	}
	diagnostics = append(diagnostics, model.Diagnostics...)
	needRasql, needTime := false, false
	for _, object := range out.Objects {
		for _, column := range object.Columns {
			if column.Scalar == "time" {
				needTime = true
			}
			if column.Nullable {
				needRasql = true
			}
		}
	}
	for _, query := range out.Queries {
		for _, field := range query.Parameters {
			if field.Type == "time.Time" || field.Type == "rasql.Nullable[time.Time]" {
				needTime = true
			}
			if field.Nullable {
				needRasql = true
			}
		}
		if query.Result != nil {
			for _, field := range query.Result.Fields {
				if field.Type == "time.Time" || field.Type == "rasql.Nullable[time.Time]" {
					needTime = true
				}
				if field.Nullable {
					needRasql = true
				}
			}
		}
	}
	if needRasql {
		out.Imports = append(out.Imports, GoImport{Path: "github.com/lestrrat-go/rasql"})
	}
	if needTime {
		out.Imports = append(out.Imports, GoImport{Path: "time"})
	}
	out.Imports = dedupImports(out.Imports)
	if err := ValidateGo(out); err != nil {
		diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_go", Path: "go", Message: err.Error()})
	}
	diagnostics = sortDiagnostics(diagnostics)
	return out, append([]Diagnostic(nil), diagnostics...)
}
func dedupImports(in []GoImport) []GoImport {
	out := make([]GoImport, 0, len(in))
	seen := map[string]struct{}{}
	for _, imp := range in {
		key := imp.Path + "\x00" + imp.Alias
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, imp)
	}
	return out
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
		return ""
	}
	if nullable {
		return "rasql.Nullable[" + base + "]"
	}
	return base
}
func (m GoModel) Validate() error { return ValidateGo(m) }
