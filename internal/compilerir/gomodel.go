package compilerir

type GoModel struct {
	Package string
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
	Emitter         string
	Prune           bool
}

func BuildGo(model SemanticModel, config GoConfig) (GoModel, []Diagnostic) {
	out := GoModel{Package: config.Package}
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
		for _, column := range object.Columns {
			goObject.Columns = append(goObject.Columns, GoColumn{Name: column.Name, PhysicalName: column.Name, Scalar: column.Scalar, GoType: goType(column.Scalar, column.Nullable), Nullable: column.Nullable})
		}
		out.Objects = append(out.Objects, goObject)
	}
	for _, query := range model.Queries {
		out.Queries = append(out.Queries, GoQuery{ID: query.ID, Name: query.Name, Cardinality: query.Cardinality})
	}
	return out, model.Diagnostics
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
