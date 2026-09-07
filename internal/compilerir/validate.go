package compilerir

import (
	"fmt"
	"go/token"
	"sort"
)

func ValidatePhysical(c PhysicalCatalog) error {
	if c.Engine.Dialect != "postgresql" && c.Engine.Dialect != "mysql" && c.Engine.Dialect != "sqlite" {
		return invalid("engine.dialect", "unsupported dialect %q", c.Engine.Dialect)
	}
	seen := map[ObjectID]struct{}{}
	names := map[QualifiedName]struct{}{}
	for i, o := range c.Objects {
		if o.ID == "" {
			return invalid(fmt.Sprintf("objects[%d].id", i), "must not be empty")
		}
		if _, ok := seen[o.ID]; ok {
			return invalid(fmt.Sprintf("objects[%d].id", i), "duplicate object ID")
		}
		seen[o.ID] = struct{}{}
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
		}
	}
	return nil
}
func ValidateSemantic(m SemanticModel) error {
	for i, o := range m.Objects {
		if o.ID == "" {
			return invalid(fmt.Sprintf("objects[%d].id", i), "must not be empty")
		}
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
	}
	return nil
}
func ValidateGo(m GoModel) error {
	if m.Package == "" || !token.IsIdentifier(m.Package) || m.Package == "_" {
		return invalid("package", "must be a valid package identifier")
	}
	files := map[string]struct{}{}
	for i, f := range m.Files {
		if f.Path == "" || f.Path[0] == '/' {
			return invalid(fmt.Sprintf("files[%d].path", i), "must be a relative path")
		}
		if _, ok := files[f.Path]; ok {
			return invalid(fmt.Sprintf("files[%d].path", i), "duplicate file")
		}
		files[f.Path] = struct{}{}
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
