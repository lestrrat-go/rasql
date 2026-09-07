package compilerir

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"
)

func ValidatePhysical(c PhysicalCatalog) error {
	if c.Engine.Dialect != "postgresql" && c.Engine.Dialect != "mysql" && c.Engine.Dialect != "sqlite" {
		return invalid("engine.dialect", "unsupported dialect %q", c.Engine.Dialect)
	}
	seen := map[ObjectID]struct{}{}
	names := map[QualifiedName]struct{}{}
	objectColumns := map[QualifiedName]map[string]struct{}{}
	for _, o := range c.Objects {
		q := QualifiedName{o.Schema, o.Name}
		columns := map[string]struct{}{}
		for _, col := range o.Columns {
			columns[col.Name] = struct{}{}
		}
		objectColumns[q] = columns
	}
	primaryCounts := map[QualifiedName]int{}
	for i, o := range c.Objects {
		if o.ID == "" {
			return invalid(fmt.Sprintf("objects[%d].id", i), "must not be empty")
		}
		if _, ok := seen[o.ID]; ok {
			return invalid(fmt.Sprintf("objects[%d].id", i), "duplicate object ID")
		}
		seen[o.ID] = struct{}{}
		if o.Kind != "table" && o.Kind != "view" {
			return invalid(fmt.Sprintf("objects[%d].kind", i), "must be table or view")
		}
		if o.Name == "" {
			return invalid(fmt.Sprintf("objects[%d].name", i), "must not be empty")
		}
		q := QualifiedName{o.Schema, o.Name}
		if _, ok := names[q]; ok {
			return invalid(fmt.Sprintf("objects[%d]", i), "duplicate qualified name")
		}
		names[q] = struct{}{}
		for j, col := range o.Columns {
			if col.Ordinal != j {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].ordinal", i, j), "must be contiguous")
			}
			if col.Name == "" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].name", i, j), "must not be empty")
			}
			switch col.LogicalKind {
			case "boolean", "integer", "float", "text", "bytes", "time", "json", "uuid", "decimal", "native":
			default:
				return invalid(fmt.Sprintf("objects[%d].columns[%d].logical_kind", i, j), "unsupported logical kind")
			}
			for k := j + 1; k < len(o.Columns); k++ {
				if o.Columns[k].Name == col.Name {
					return invalid(fmt.Sprintf("objects[%d].columns[%d].name", i, k), "duplicate column")
				}
			}
			if col.Native != nil && col.Native.Name != "" && col.Native.Dialect == "" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].native.dialect", i, j), "must not be empty")
			}
			if col.Native != nil {
				if err := validateNative(col.Native, fmt.Sprintf("objects[%d].columns[%d].native", i, j)); err != nil {
					return err
				}
			}
			if col.Integer != nil && col.LogicalKind != "integer" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].integer", i, j), "facts do not match logical kind")
			}
			if col.Text != nil && col.LogicalKind != "text" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].text", i, j), "facts do not match logical kind")
			}
			if col.Decimal != nil && col.LogicalKind != "decimal" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].decimal", i, j), "facts do not match logical kind")
			}
			if col.Integer != nil && col.Integer.DisplayWidth.Set && col.Integer.DisplayWidth.Value < 0 {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].integer.display_width", i, j), "must not be negative")
			}
			if col.Text != nil && col.Text.Width.Set && col.Text.Width.Value < 0 {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].text.width", i, j), "must not be negative")
			}
			if col.Decimal != nil && (col.Decimal.Precision <= 0 || (col.Decimal.Scale.Set && (col.Decimal.Scale.Value < 0 || col.Decimal.Scale.Value > col.Decimal.Precision))) {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].decimal", i, j), "invalid precision or scale")
			}
			if col.Identity != "" && col.Identity != "ALWAYS" && col.Identity != "BY DEFAULT" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].identity", i, j), "unsupported identity generation")
			}
			if col.GeneratedStorage != "" && col.GeneratedStorage != "STORED" && col.GeneratedStorage != "VIRTUAL" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].generated_storage", i, j), "unsupported generated storage")
			}
		}
		columns := map[string]struct{}{}
		for _, col := range o.Columns {
			columns[col.Name] = struct{}{}
		}
		objectColumns[q] = columns
		for j, constraint := range o.Constraints {
			if constraint.Kind != "primary_key" && constraint.Kind != "unique" && constraint.Kind != "foreign_key" && constraint.Kind != "check" {
				return invalid(fmt.Sprintf("objects[%d].constraints[%d].kind", i, j), "unsupported constraint kind")
			}
			for _, name := range constraint.Columns {
				if _, ok := columns[name]; !ok {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d]", i, j), "references unknown column")
				}
			}
			if constraint.Kind == "primary_key" {
				primaryCounts[q]++
			}
			if constraint.Kind == "foreign_key" && constraint.Reference == nil {
				return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference", i, j), "foreign key reference is required")
			}
			if constraint.Reference != nil {
				if constraint.Reference.Object == "" || len(constraint.Reference.Columns) == 0 {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference", i, j), "incomplete foreign reference")
				}
				ref, targetColumns, ok := resolveForeignReference(c.Engine, o.Schema, *constraint.Reference, objectColumns)
				_ = ref
				if !ok {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference", i, j), "references unknown object")
				}
				if len(constraint.Columns) != len(constraint.Reference.Columns) {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference.columns", i, j), "column count does not match foreign key")
				}
				for _, name := range constraint.Reference.Columns {
					if _, ok := targetColumns[name]; !ok {
						return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference", i, j), "references unknown column")
					}
				}
			}
		}
		constraintNames := map[string]struct{}{}
		for j, constraint := range o.Constraints {
			if constraint.Name != "" {
				if _, ok := constraintNames[constraint.Name]; ok {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d].name", i, j), "duplicate constraint name")
				}
				constraintNames[constraint.Name] = struct{}{}
			}
		}
		indexNames := map[string]struct{}{}
		for j, index := range o.Indexes {
			if index.Name == "" {
				return invalid(fmt.Sprintf("objects[%d].indexes[%d].name", i, j), "must not be empty")
			}
			if index.KeyForm != "columns" && index.KeyForm != "expressions" && index.KeyForm != "keys" {
				return invalid(fmt.Sprintf("objects[%d].indexes[%d].key_form", i, j), "must be columns, expressions, or keys")
			}
			if _, ok := indexNames[index.Name]; ok {
				return invalid(fmt.Sprintf("objects[%d].indexes[%d].name", i, j), "duplicate index name")
			}
			indexNames[index.Name] = struct{}{}
			if len(index.Parts) == 0 {
				return invalid(fmt.Sprintf("objects[%d].indexes[%d].parts", i, j), "must not be empty")
			}
			for k, part := range index.Parts {
				p := fmt.Sprintf("objects[%d].indexes[%d].parts[%d]", i, j, k)
				if part.Column != "" && part.ExpressionSQL != "" {
					return invalid(p, "column and expression are mutually exclusive")
				}
				switch index.KeyForm {
				case "columns":
					if part.Column == "" || part.ExpressionSQL != "" || part.Direction != "" || part.Nulls != "" || part.Collation != "" || part.OperatorClass != "" || part.PrefixLength != 0 {
						return invalid(p, "columns form requires plain column parts")
					}
					if _, ok := columns[part.Column]; !ok {
						return invalid(p+".column", "references unknown column")
					}
				case "expressions":
					if part.ExpressionSQL == "" || part.Direction != "" || part.Nulls != "" || part.Collation != "" || part.OperatorClass != "" || part.PrefixLength != 0 {
						return invalid(p, "expressions form requires plain expression parts")
					}
				case "keys":
					if part.Column == "" && part.ExpressionSQL == "" {
						return invalid(p, "key part must have column or expression")
					}
					if part.Direction != "" && part.Direction != "ASC" && part.Direction != "DESC" {
						return invalid(p+".direction", "must be ASC or DESC")
					}
					if part.Nulls != "" && part.Nulls != "FIRST" && part.Nulls != "LAST" {
						return invalid(p+".nulls", "must be FIRST or LAST")
					}
					if part.PrefixLength < 0 {
						return invalid(p+".prefix_length", "must not be negative")
					}
					if part.Column != "" {
						if _, ok := columns[part.Column]; !ok {
							return invalid(p+".column", "references unknown column")
						}
					}
				}
			}
		}
		for j, exclusion := range o.ExclusionConstraints {
			if exclusion.Name == "" {
				return invalid(fmt.Sprintf("objects[%d].exclusion_constraints[%d].name", i, j), "must not be empty")
			}
			if len(exclusion.Elements) == 0 {
				return invalid(fmt.Sprintf("objects[%d].exclusion_constraints[%d].elements", i, j), "must not be empty")
			}
			for k, element := range exclusion.Elements {
				if element.ExpressionSQL == "" {
					return invalid(fmt.Sprintf("objects[%d].exclusion_constraints[%d].elements[%d].expression_sql", i, j, k), "must not be empty")
				}
			}
		}
	}
	for q, count := range primaryCounts {
		if count > 1 {
			return invalid("objects", "multiple primary keys for %s.%s", q.Schema, q.Name)
		}
	}
	return nil
}
func validateNative(n *NativeType, p string) error {
	if n == nil {
		return nil
	}
	if n.Dialect != "postgresql" && n.Dialect != "mysql" && n.Dialect != "sqlite" {
		return invalid(p+".dialect", "unsupported dialect")
	}
	if n.Name == "" {
		return invalid(p+".name", "must not be empty")
	}
	if n.Kind != "builtin" && n.Kind != "domain" && n.Kind != "enum" && n.Kind != "set" && n.Kind != "array" && n.Kind != "other" {
		return invalid(p+".kind", "unsupported native kind")
	}
	if n.Kind == "array" && n.Element == nil {
		return invalid(p+".element", "array native type requires an element")
	}
	if n.Dialect == "" {
		return invalid(p+".dialect", "must not be empty")
	}
	return validateNative(n.Element, p+".element")
}
func resolveForeignReference(engine EngineIdentity, sourceSchema string, ref ForeignReference, objects map[QualifiedName]map[string]struct{}) (QualifiedName, map[string]struct{}, bool) {
	q := QualifiedName{Schema: ref.Schema, Name: ref.Object}
	if q.Schema == "" && engine.Dialect == "sqlite" {
		q.Schema = sourceSchema
		if q.Schema == "" {
			q.Schema = "main"
		}
	}
	if columns, ok := objects[q]; ok {
		return q, columns, true
	}
	if engine.Dialect == "sqlite" && q.Schema == "main" {
		if columns, ok := objects[QualifiedName{Name: q.Name}]; ok {
			return QualifiedName{Name: q.Name}, columns, true
		}
	}
	if ref.Schema != "" {
		return q, nil, false
	}
	if engine.Dialect == "sqlite" {
		return q, nil, false
	}
	var found QualifiedName
	var columns map[string]struct{}
	for candidate, candidateColumns := range objects {
		if candidate.Name != ref.Object {
			continue
		}
		if columns != nil {
			return q, nil, false
		}
		found, columns = candidate, candidateColumns
	}
	if columns == nil {
		return q, nil, false
	}
	return found, columns, true
}
func ValidateSemantic(m SemanticModel) error {
	ids := map[ObjectID]struct{}{}
	names := map[QualifiedName]struct{}{}
	knownIDs := map[ObjectID]struct{}{}
	for _, o := range m.Objects {
		knownIDs[o.ID] = struct{}{}
	}
	for i, o := range m.Objects {
		if o.ID == "" {
			return invalid(fmt.Sprintf("objects[%d].id", i), "must not be empty")
		}
		if _, ok := ids[o.ID]; ok {
			return invalid(fmt.Sprintf("objects[%d].id", i), "duplicate object ID")
		}
		ids[o.ID] = struct{}{}
		if _, ok := names[o.PhysicalName]; ok {
			return invalid(fmt.Sprintf("objects[%d].physical_name", i), "duplicate physical name")
		}
		names[o.PhysicalName] = struct{}{}
		for j, c := range o.Columns {
			if c.Name == "" || c.Scalar == "" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d]", i, j), "name and scalar are required")
			}
			if c.InsertState != "required" && c.InsertState != "optional" && c.InsertState != "generated" && c.InsertState != "forbidden" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].insert_state", i, j), "unknown state")
			}
			if c.PatchState != "settable" && c.PatchState != "forbidden" {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].patch_state", i, j), "unknown state")
			}
			if c.Certainty != CertaintyKnown && c.Certainty != CertaintyDeclared && c.Certainty != CertaintyUnknown {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].certainty", i, j), "unknown certainty")
			}
		}
		for j, relation := range o.Relations {
			if relation.Name == "" || relation.Target == "" || len(relation.From) == 0 || len(relation.To) == 0 || len(relation.From) != len(relation.To) {
				return invalid(fmt.Sprintf("objects[%d].relations[%d]", i, j), "incomplete relation")
			}
			if _, ok := knownIDs[relation.Target]; !ok {
				return invalid(fmt.Sprintf("objects[%d].relations[%d].target", i, j), "unknown target")
			}
		}
	}
	queryIDs := map[QueryID]struct{}{}
	for i, q := range m.Queries {
		if q.ID == "" || q.Name == "" {
			return invalid(fmt.Sprintf("queries[%d]", i), "ID and name are required")
		}
		if !token.IsIdentifier(q.Name) {
			return invalid(fmt.Sprintf("queries[%d].name", i), "must be a valid identifier")
		}
		if q.Cardinality != "one" && q.Cardinality != "maybe" && q.Cardinality != "many" && q.Cardinality != "exec" {
			return invalid(fmt.Sprintf("queries[%d].cardinality", i), "unknown cardinality")
		}
		if _, ok := queryIDs[q.ID]; ok {
			return invalid(fmt.Sprintf("queries[%d].id", i), "duplicate query ID")
		}
		queryIDs[q.ID] = struct{}{}
		values := append(append([]SemanticValue{}, q.Parameters...), q.Results...)
		for j, value := range values {
			if err := validateSemanticValue(value, fmt.Sprintf("queries[%d].values[%d]", i, j)); err != nil {
				return err
			}
		}
	}
	for i, diagnostic := range m.Diagnostics {
		if diagnostic.Level != DiagnosticError && diagnostic.Level != DiagnosticWarning {
			return invalid(fmt.Sprintf("diagnostics[%d].level", i), "unknown diagnostic level")
		}
	}
	return nil
}

func validateSemanticValue(value SemanticValue, path string) error {
	if value.Name == "" || value.Scalar == "" {
		return invalid(path, "name and scalar are required")
	}
	if !validCertainty(value.TypeCertainty) || !validCertainty(value.NullabilityCertainty) {
		return invalid(path, "unknown certainty")
	}
	if value.Integer != nil {
		if value.LogicalKind != "integer" {
			return invalid(path+".integer", "facts do not match logical kind")
		}
		if value.Integer.DisplayWidth.Set && value.Integer.DisplayWidth.Value < 0 {
			return invalid(path+".integer.display_width", "must not be negative")
		}
	}
	if value.Native != nil {
		if err := validateNative(value.Native, path+".native"); err != nil {
			return err
		}
	}
	if value.TypeCertainty == CertaintyKnown && value.Scalar == "" && value.LogicalKind == "" && value.Native == nil {
		return invalid(path+".type_certainty", "known type requires logical kind or native descriptor")
	}
	return nil
}
func validCertainty(c Certainty) bool {
	return c == CertaintyKnown || c == CertaintyDeclared || c == CertaintyUnknown
}
func ValidateGo(m GoModel) error {
	if m.Package == "" || !token.IsIdentifier(m.Package) || m.Package == "_" {
		return invalid("package", "must be a valid package identifier")
	}
	files := map[string]struct{}{}
	imports := map[string]struct{}{}
	importNames := map[string]struct{}{}
	for i, imp := range m.Imports {
		if imp.Path == "" {
			return invalid(fmt.Sprintf("imports[%d].path", i), "must not be empty")
		}
		if _, ok := imports[imp.Path+"\x00"+imp.Alias]; ok {
			return invalid(fmt.Sprintf("imports[%d]", i), "duplicate import")
		}
		if imp.Alias != "" && (!token.IsIdentifier(imp.Alias) || imp.Alias == "_" || imp.Alias == ".") {
			return invalid(fmt.Sprintf("imports[%d].alias", i), "must be a valid identifier")
		}
		imports[imp.Path+"\x00"+imp.Alias] = struct{}{}
		alias := imp.Alias
		if alias == "" {
			alias = path.Base(imp.Path)
		}
		importNames[alias] = struct{}{}
	}
	objects := map[ObjectID]struct{}{}
	knownObjects := map[ObjectID]struct{}{}
	for _, object := range m.Objects {
		knownObjects[object.ID] = struct{}{}
	}
	for i, f := range m.Files {
		if f.Path == "" || f.Path[0] == '/' || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "../") || f.Path == ".." {
			return invalid(fmt.Sprintf("files[%d].path", i), "must be a relative path")
		}
		if _, ok := files[f.Path]; ok {
			return invalid(fmt.Sprintf("files[%d].path", i), "duplicate file")
		}
		files[f.Path] = struct{}{}
	}
	for i, object := range m.Objects {
		if _, ok := objects[object.ID]; ok {
			return invalid(fmt.Sprintf("objects[%d].id", i), "duplicate object ID")
		}
		objects[object.ID] = struct{}{}
		if object.ID == "" || object.SourceName == "" || !token.IsIdentifier(object.SourceName) || object.Row.Name == "" || !token.IsIdentifier(object.Row.Name) {
			return invalid(fmt.Sprintf("objects[%d]", i), "invalid object or row name")
		}
		if object.Row.DecoderName == "" || !token.IsIdentifier(object.Row.DecoderName) {
			return invalid(fmt.Sprintf("objects[%d].row.decoder_name", i), "invalid decoder name")
		}
		if object.Create == nil || object.Patch == nil {
			return invalid(fmt.Sprintf("objects[%d]", i), "create and patch shapes are required")
		}
		for j, field := range object.Row.Fields {
			if err := validateGoField(field, importNames); err != nil {
				return invalid(fmt.Sprintf("objects[%d].row.fields[%d]", i, j), "%v", err)
			}
		}
		for shapeName, shape := range map[string]*GoShape{"create": object.Create, "patch": object.Patch} {
			if shape.Name == "" || !token.IsIdentifier(shape.Name) || shape.DecoderName != "" && !token.IsIdentifier(shape.DecoderName) {
				return invalid(fmt.Sprintf("objects[%d].%s", i, shapeName), "invalid shape name")
			}
			for j, field := range shape.Fields {
				if err := validateGoField(field, importNames); err != nil {
					return invalid(fmt.Sprintf("objects[%d].%s.fields[%d]", i, shapeName, j), "%v", err)
				}
			}
		}
		for j, column := range object.Columns {
			if column.Name == "" || column.GoType == "" || !token.IsIdentifier(column.Name) {
				return invalid(fmt.Sprintf("objects[%d].columns[%d]", i, j), "invalid column")
			}
			if err := parseGoType(column.GoType, importNames); err != nil {
				return invalid(fmt.Sprintf("objects[%d].columns[%d].go_type", i, j), "%v", err)
			}
		}
		states := make(map[string]struct{ insert, patch string }, len(object.Columns))
		for _, column := range object.Columns {
			states[column.Name] = struct{ insert, patch string }{insert: column.InsertState, patch: column.PatchState}
		}
		for _, field := range object.Create.Fields {
			if state, ok := states[field.Name]; ok && (state.insert == "generated" || state.insert == "forbidden") {
				return invalid(fmt.Sprintf("objects[%d].create.%s", i, field.Name), "generated or forbidden column is caller-writable")
			}
		}
		for _, field := range object.Patch.Fields {
			if state, ok := states[field.Name]; ok && state.patch == "forbidden" {
				return invalid(fmt.Sprintf("objects[%d].patch.%s", i, field.Name), "forbidden column is caller-writable")
			}
		}
		for j, relation := range object.Relations {
			if relation.Name == "" || relation.Target == "" {
				return invalid(fmt.Sprintf("objects[%d].relations[%d]", i, j), "invalid relation")
			}
			if _, ok := knownObjects[relation.Target]; !ok {
				return invalid(fmt.Sprintf("objects[%d].relations[%d].target", i, j), "unknown target")
			}
		}
	}
	queries := map[QueryID]struct{}{}
	for i, query := range m.Queries {
		if query.ID == "" || query.Name == "" {
			return invalid(fmt.Sprintf("queries[%d]", i), "invalid query")
		}
		if !token.IsIdentifier(query.Name) {
			return invalid(fmt.Sprintf("queries[%d].name", i), "must be a valid identifier")
		}
		if query.Cardinality != "one" && query.Cardinality != "maybe" && query.Cardinality != "many" && query.Cardinality != "exec" {
			return invalid(fmt.Sprintf("queries[%d].cardinality", i), "unknown cardinality")
		}
		for j, field := range query.Parameters {
			if err := validateGoField(field, importNames); err != nil {
				return invalid(fmt.Sprintf("queries[%d].parameters[%d]", i, j), "%v", err)
			}
		}
		if _, ok := queries[query.ID]; ok {
			return invalid(fmt.Sprintf("queries[%d].id", i), "duplicate query ID")
		}
		queries[query.ID] = struct{}{}
		if query.Result != nil {
			if query.Result.Name == "" || !token.IsIdentifier(query.Result.Name) {
				return invalid(fmt.Sprintf("queries[%d].result.name", i), "invalid result name")
			}
			if query.Result.DecoderName == "" || !token.IsIdentifier(query.Result.DecoderName) {
				return invalid(fmt.Sprintf("queries[%d].result.decoder_name", i), "invalid decoder name")
			}
			for j, field := range query.Result.Fields {
				if err := validateGoField(field, importNames); err != nil {
					return invalid(fmt.Sprintf("queries[%d].result.fields[%d]", i, j), "%v", err)
				}
			}
		}
	}
	if err := validateGoExpressions(goModelExpressions(m), m.Imports, m.Package); err != nil {
		return invalid("types", "%v", err)
	}
	return nil
}

func goModelExpressions(m GoModel) []string {
	var expressions []string
	for _, object := range m.Objects {
		for _, column := range object.Columns {
			expressions = append(expressions, column.GoType)
		}
		for _, field := range object.Row.Fields {
			expressions = append(expressions, field.Type)
		}
		for _, shape := range []*GoShape{object.Create, object.Patch} {
			if shape == nil {
				continue
			}
			for _, field := range shape.Fields {
				expressions = append(expressions, field.Type)
			}
		}
	}
	for _, query := range m.Queries {
		for _, field := range query.Parameters {
			expressions = append(expressions, field.Type)
		}
		if query.Result != nil {
			for _, field := range query.Result.Fields {
				expressions = append(expressions, field.Type)
			}
		}
	}
	return expressions
}
func validateGoField(field GoField, imports map[string]struct{}) error {
	if field.Name == "" || !token.IsIdentifier(field.Name) || field.Type == "" {
		return fmt.Errorf("invalid field")
	}
	if !field.Nullable && strings.HasPrefix(field.Type, "rasql.Nullable[") {
		return fmt.Errorf("non-null field cannot use nullable type")
	}
	return parseGoType(field.Type, imports)
}
func parseGoType(s string, imports map[string]struct{}) error {
	expr, err := parser.ParseExpr(s)
	if err != nil {
		return fmt.Errorf("invalid Go type %q", s)
	}
	var check func(ast.Expr) error
	check = func(node ast.Expr) error {
		switch n := node.(type) {
		case *ast.Ident:
			return nil
		case *ast.SelectorExpr:
			x, ok := n.X.(*ast.Ident)
			if !ok {
				return fmt.Errorf("invalid selector")
			}
			if _, ok := imports[x.Name]; !ok {
				return fmt.Errorf("unresolved import %q", x.Name)
			}
		case *ast.ArrayType:
			if n.Len != nil {
				if _, ok := n.Len.(*ast.BasicLit); !ok {
					return fmt.Errorf("invalid array length")
				}
			}
			return check(n.Elt)
		case *ast.StarExpr:
			return check(n.X)
		case *ast.MapType:
			if err := check(n.Key); err != nil {
				return err
			}
			return check(n.Value)
		case *ast.ParenExpr:
			return check(n.X)
		case *ast.IndexExpr:
			if err := check(n.X); err != nil {
				return err
			}
			return check(n.Index)
		case *ast.IndexListExpr:
			if err := check(n.X); err != nil {
				return err
			}
			for _, index := range n.Indices {
				if err := check(index); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("expression is not a Go type")
		}
		return nil
	}
	if err := check(expr); err != nil {
		return err
	}
	return nil
}

func validateGoExpressions(expressions []string, imports []GoImport, packageName string) error {
	if packageName == "" || !token.IsIdentifier(packageName) || packageName == "_" {
		return fmt.Errorf("invalid package name %q", packageName)
	}
	importNames := make(map[string]struct{}, len(imports))
	paths := make(map[string]struct{}, len(imports))
	for i, imp := range imports {
		if imp.Path == "" {
			return fmt.Errorf("imports[%d]: import path must not be empty", i)
		}
		if _, ok := paths[imp.Path]; ok {
			return fmt.Errorf("imports[%d]: duplicate import path %q", i, imp.Path)
		}
		paths[imp.Path] = struct{}{}
		alias := imp.Alias
		if alias == "" {
			alias = path.Base(imp.Path)
		}
		if !token.IsIdentifier(alias) || alias == "_" || alias == "." {
			return fmt.Errorf("imports[%d]: invalid import alias %q", i, alias)
		}
		if _, ok := importNames[alias]; ok {
			return fmt.Errorf("imports[%d]: duplicate effective import name %q", i, alias)
		}
		importNames[alias] = struct{}{}
	}
	used := make(map[string]struct{}, len(importNames))
	for _, expression := range expressions {
		expr, err := parser.ParseExpr(expression)
		if err != nil {
			return fmt.Errorf("invalid Go type %q", expression)
		}
		if err := validateGoTypeAST(expr, importNames, used); err != nil {
			return err
		}
	}
	for name := range importNames {
		if _, ok := used[name]; !ok {
			return fmt.Errorf("unused import %q", name)
		}
	}
	return nil
}

func validateGoTypeAST(expr ast.Expr, imports, used map[string]struct{}) error {
	var check func(ast.Expr) error
	check = func(node ast.Expr) error {
		switch n := node.(type) {
		case *ast.Ident:
			return nil
		case *ast.SelectorExpr:
			x, ok := n.X.(*ast.Ident)
			if !ok {
				return fmt.Errorf("invalid selector")
			}
			if _, ok := imports[x.Name]; !ok {
				return fmt.Errorf("unresolved import %q", x.Name)
			}
			used[x.Name] = struct{}{}
		case *ast.ArrayType:
			if n.Len != nil {
				if _, ok := n.Len.(*ast.BasicLit); !ok {
					return fmt.Errorf("invalid array length")
				}
			}
			return check(n.Elt)
		case *ast.StarExpr:
			return check(n.X)
		case *ast.MapType:
			if err := check(n.Key); err != nil {
				return err
			}
			return check(n.Value)
		case *ast.ParenExpr:
			return check(n.X)
		case *ast.IndexExpr:
			if err := check(n.X); err != nil {
				return err
			}
			return check(n.Index)
		case *ast.IndexListExpr:
			if err := check(n.X); err != nil {
				return err
			}
			for _, index := range n.Indices {
				if err := check(index); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("expression is not a Go type")
		}
		return nil
	}
	return check(expr)
}
func sortDiagnostics(d []Diagnostic) []Diagnostic {
	out := append([]Diagnostic(nil), d...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Path < out[j].Path
	})
	return out
}
