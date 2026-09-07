package compilerir

import (
	"fmt"
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
			if constraint.Reference != nil {
				if constraint.Reference.Object == "" || len(constraint.Reference.Columns) == 0 {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference", i, j), "incomplete foreign reference")
				}
				targetColumns, ok := objectColumns[QualifiedName{Schema: constraint.Reference.Schema, Name: constraint.Reference.Object}]
				if !ok {
					for candidate := range names {
						if candidate.Name == constraint.Reference.Object && (constraint.Reference.Schema == "" || candidate.Schema == constraint.Reference.Schema) {
							targetColumns = objectColumns[candidate]
							ok = true
							break
						}
					}
				}
				if !ok {
					return invalid(fmt.Sprintf("objects[%d].constraints[%d].reference", i, j), "references unknown object")
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
			for _, part := range index.Parts {
				if part.Column == "" && part.ExpressionSQL == "" {
					return invalid(fmt.Sprintf("objects[%d].indexes[%d]", i, j), "index part must have column or expression")
				}
				if index.KeyForm == "columns" && (part.Column == "" || part.ExpressionSQL != "") {
					return invalid(fmt.Sprintf("objects[%d].indexes[%d].parts", i, j), "column key form requires column parts")
				}
				if index.KeyForm != "columns" && part.ExpressionSQL == "" {
					return invalid(fmt.Sprintf("objects[%d].indexes[%d].parts", i, j), "expression and key forms require expression parts")
				}
			}
		}
	}
	return nil
}
func validateNative(n *NativeType, p string) error {
	if n == nil {
		return nil
	}
	if n.Name != "" && n.Dialect == "" {
		return invalid(p+".dialect", "must not be empty")
	}
	return validateNative(n.Element, p+".element")
}
func ValidateSemantic(m SemanticModel) error {
	ids := map[ObjectID]struct{}{}
	names := map[QualifiedName]struct{}{}
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
			if relation.Name == "" || relation.Target == "" || len(relation.From) == 0 || len(relation.To) == 0 {
				return invalid(fmt.Sprintf("objects[%d].relations[%d]", i, j), "incomplete relation")
			}
		}
	}
	queryIDs := map[QueryID]struct{}{}
	for i, q := range m.Queries {
		if q.ID == "" || q.Name == "" {
			return invalid(fmt.Sprintf("queries[%d]", i), "ID and name are required")
		}
		if _, ok := queryIDs[q.ID]; ok {
			return invalid(fmt.Sprintf("queries[%d].id", i), "duplicate query ID")
		}
		queryIDs[q.ID] = struct{}{}
		for j, value := range append(append([]SemanticValue{}, q.Parameters...), q.Results...) {
			if value.Name == "" || value.Scalar == "" {
				return invalid(fmt.Sprintf("queries[%d].values[%d]", i, j), "name and scalar are required")
			}
			if !validCertainty(value.TypeCertainty) || !validCertainty(value.NullabilityCertainty) {
				return invalid(fmt.Sprintf("queries[%d].values[%d]", i, j), "unknown certainty")
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
func validCertainty(c Certainty) bool {
	return c == CertaintyKnown || c == CertaintyDeclared || c == CertaintyUnknown
}
func ValidateGo(m GoModel) error {
	if m.Package == "" || !token.IsIdentifier(m.Package) || m.Package == "_" {
		return invalid("package", "must be a valid package identifier")
	}
	files := map[string]struct{}{}
	imports := map[string]struct{}{}
	for i, imp := range m.Imports {
		if imp.Path == "" {
			return invalid(fmt.Sprintf("imports[%d].path", i), "must not be empty")
		}
		if _, ok := imports[imp.Path+"\x00"+imp.Alias]; ok {
			return invalid(fmt.Sprintf("imports[%d]", i), "duplicate import")
		}
		imports[imp.Path+"\x00"+imp.Alias] = struct{}{}
	}
	objects := map[ObjectID]struct{}{}
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
		if object.ID == "" || object.SourceName == "" || object.Row.Name == "" || !token.IsIdentifier(object.Row.Name) {
			return invalid(fmt.Sprintf("objects[%d]", i), "invalid object or row name")
		}
		for j, column := range object.Columns {
			if column.Name == "" || column.GoType == "" || !token.IsIdentifier(column.Name) {
				return invalid(fmt.Sprintf("objects[%d].columns[%d]", i, j), "invalid column")
			}
		}
		for j, relation := range object.Relations {
			if relation.Name == "" || relation.Target == "" {
				return invalid(fmt.Sprintf("objects[%d].relations[%d]", i, j), "invalid relation")
			}
		}
	}
	queries := map[QueryID]struct{}{}
	for i, query := range m.Queries {
		if query.ID == "" || query.Name == "" {
			return invalid(fmt.Sprintf("queries[%d]", i), "invalid query")
		}
		if _, ok := queries[query.ID]; ok {
			return invalid(fmt.Sprintf("queries[%d].id", i), "duplicate query ID")
		}
		queries[query.ID] = struct{}{}
		if query.Result != nil {
			for j, field := range query.Result.Fields {
				if field.Name == "" || field.Type == "" {
					return invalid(fmt.Sprintf("queries[%d].result.fields[%d]", i, j), "invalid field")
				}
			}
		}
	}
	return nil
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
