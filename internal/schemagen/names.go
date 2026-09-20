package schemagen

import (
	"fmt"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

// compactOmissions records the few places one table's generated file cannot
// spell a column the plain way, because Go forbids two members of one type
// sharing a name. Everything not named here is generated exactly as it always
// was.
//
// The generator's rule is that rasql's own conveniences give way to the
// caller's columns. ScanRow below is the whole of the other direction: a
// builder's terminal method cannot move aside, because it is how the statement
// is run.
type compactOmissions struct {
	// Columns are left out of the generated surface altogether. A column
	// lands here only when an earlier column of the same table already took
	// the Go name it wants, leaving it none of its own. Ref().Column reaches
	// it, and nothing else does.
	Columns map[string]struct{}
	// CreateSetters and PatchSetters name columns kept everywhere except for
	// one setter on one builder, whose name is that builder's own terminal or
	// was taken by an earlier column's Clear or Default setter. The column is
	// still read, still a field, and still written through rasql.SetField.
	CreateSetters map[string]struct{}
	PatchSetters  map[string]struct{}
	// ClearSetters and DefaultSetters name columns whose Clear or Default
	// setter alone collided, on both builders.
	ClearSetters   map[string]struct{}
	DefaultSetters map[string]struct{}
	// ScanRow is true when a column is named ScanRow, so the generated row
	// type carries the caller's column and not rasql's own scan method.
	ScanRow bool
}

func newCompactOmissions() compactOmissions {
	return compactOmissions{
		Columns:        map[string]struct{}{},
		CreateSetters:  map[string]struct{}{},
		PatchSetters:   map[string]struct{}{},
		ClearSetters:   map[string]struct{}{},
		DefaultSetters: map[string]struct{}{},
	}
}

func (o compactOmissions) omits(set map[string]struct{}, column string) bool {
	if _, gone := o.Columns[column]; gone {
		return true
	}
	_, gone := set[column]
	return gone
}

// SkipsColumn reports a column left out of the generated surface entirely.
func (o compactOmissions) SkipsColumn(column string) bool {
	_, gone := o.Columns[column]
	return gone
}

// SkipsCreateSetter and the three below report one builder member left out for
// a column the rest of the file still carries.
func (o compactOmissions) SkipsCreateSetter(column string) bool {
	return o.omits(o.CreateSetters, column)
}

func (o compactOmissions) SkipsPatchSetter(column string) bool {
	return o.omits(o.PatchSetters, column)
}

func (o compactOmissions) SkipsClearSetter(column string) bool {
	return o.omits(o.ClearSetters, column)
}

func (o compactOmissions) SkipsDefaultSetter(column string) bool {
	return o.omits(o.DefaultSetters, column)
}

// compactBuilderTerminals returns the method names one generated mutation
// builder spends on running the statement it has been building. A column whose
// setter would take one of these names loses that setter, because the terminal
// is the only way to finish the builder at all.
func compactBuilderTerminals(patch bool) map[string]string {
	terminals := map[string]string{
		"Plan": "the builder's own Plan method",
		"Exec": "the builder's own Exec method",
	}
	if patch {
		terminals["Where"] = "the patch builder's own Where method"
	}
	return terminals
}

// resolveCompactColumns works out what one object's generated file has to leave
// out, and reports one warning per omission.
//
// Three kinds of collision reach here, and they cost different things:
//
//   - A column named like one of the generated table's own methods, such as
//     "ref" or "create", costs nothing. The field is legal; only a selector
//     written against the table would find the method first, which every
//     generated reference avoids by naming the embedded expressions struct.
//     CONTRIBUTING.md and the generated store page tell the caller to do the
//     same.
//   - A column named "scan_row" costs rasql its own convenience: the generated
//     row type gets the caller's field and not the ScanRow method, which
//     nothing inside rasql needs.
//   - A column named like a builder terminal, or one whose Clear or Default
//     setter an earlier column already took, loses that one setter. Reads are
//     untouched and rasql.SetField still writes it.
//
// Only a column whose whole Go name an earlier column already holds is left out
// of the surface, because there is no name left for it anywhere.
func resolveCompactColumns(object CompactObject, accessor string) (compactOmissions, []compilerir.Diagnostic) {
	omissions := newCompactOmissions()
	var warnings []compilerir.Diagnostic
	warn := func(column, code, message string) {
		warnings = append(warnings, compilerir.Diagnostic{
			Level:   compilerir.DiagnosticWarning,
			Code:    code,
			Path:    compactWarningPath(object, column),
			Message: message,
		})
	}

	fields := make(map[string]string, len(object.Go.Columns))
	createNames := compactBuilderTerminals(false)
	patchNames := compactBuilderTerminals(true)
	for _, column := range object.Go.Columns {
		if !columnReadable(object.Semantic, column.Name) {
			continue
		}
		name := exportedCompact(column.Name)
		if owner, taken := fields[name]; taken {
			omissions.Columns[column.Name] = struct{}{}
			warn(column.Name, "go_name_taken", fmt.Sprintf(
				"column %q and column %q are both named %s in Go; %q keeps it and %q is left out of the generated package, reached with Ref().Column(%q)",
				owner, column.Name, name, owner, column.Name, column.Name))
			continue
		}
		fields[name] = column.Name
		if name == "ScanRow" {
			omissions.ScanRow = true
			warn(column.Name, "scan_row_method_dropped", fmt.Sprintf(
				"column %q is named ScanRow in Go, so the generated row type carries the column and not rasql's own ScanRow method; the column is read and written as usual",
				column.Name))
		}
		compactResolveSetter(&omissions, &warnings, object, column, name, createNames, patchNames, warn)
	}
	return omissions, warnings
}

// compactResolveSetter decides which of one column's builder setters survive,
// claiming each surviving name so a later column cannot take it.
func compactResolveSetter(omissions *compactOmissions, warnings *[]compilerir.Diagnostic, object CompactObject,
	column compilerir.GoColumn, name string, createNames, patchNames map[string]string, warn func(column, code, message string)) {
	claim := func(names map[string]string, candidate, owner string) (string, bool) {
		if blocker, taken := names[candidate]; taken {
			return blocker, false
		}
		names[candidate] = owner
		return "", true
	}
	owner := fmt.Sprintf("column %q", column.Name)

	createBlocker, createOK := claim(createNames, name, owner)
	patchBlocker, patchOK := claim(patchNames, name, owner)
	if !createOK {
		omissions.CreateSetters[column.Name] = struct{}{}
	}
	if !patchOK {
		omissions.PatchSetters[column.Name] = struct{}{}
	}
	if !createOK || !patchOK {
		blocker := createBlocker
		if blocker == "" {
			blocker = patchBlocker
		}
		warn(column.Name, "setter_name_taken", fmt.Sprintf(
			"column %q would give its builder a %s method, which is already %s; the setter is left out and rasql.SetField(%s.%s, value) writes the column instead",
			column.Name, name, blocker, compactLowerFirst(exportedCompact(object.Catalog.Name)), name))
	}

	if column.Nullable {
		if blocker, ok := claim(createNames, "Clear"+name, owner); !ok {
			omissions.ClearSetters[column.Name] = struct{}{}
			warn(column.Name, "setter_name_taken", fmt.Sprintf(
				"column %q would give its builder a Clear%s method, which is already %s; the setter is left out and rasql.ClearField writes the column instead",
				column.Name, name, blocker))
		} else {
			patchNames["Clear"+name] = owner
		}
	}
	if columnHasDefault(object.Table, column.Name) {
		if blocker, ok := claim(createNames, "Default"+name, owner); !ok {
			omissions.DefaultSetters[column.Name] = struct{}{}
			warn(column.Name, "setter_name_taken", fmt.Sprintf(
				"column %q would give its builder a Default%s method, which is already %s; the setter is left out and rasql.DefaultField writes the column instead",
				column.Name, name, blocker))
		} else {
			patchNames["Default"+name] = owner
		}
	}
}

// compactTargetObject rebuilds the object on the other side of a relationship
// as a CompactObject, so a declaration reaching across tables sees what that
// table's own file holds. The scalar mappings come from the object being
// written, since those are one setting for the whole package.
func compactTargetObject(ref CompactObjectRef, object CompactObject) CompactObject {
	return CompactObject{
		Catalog:        ref.Catalog,
		Semantic:       ref.Semantic,
		Go:             ref.Go,
		Generation:     ref.Generation,
		Table:          ref.Table,
		Mappings:       object.Mappings,
		ColumnBindings: ref.ColumnBindings,
	}
}

// compactWarningPath names the column a warning is about, as the reader sees it
// in their own schema: the table name, qualified by its namespace when it has
// one, and the column's physical name. It deliberately avoids the object ID,
// which the live path derives as a hash and which names nothing a reader could
// look up.
func compactWarningPath(object CompactObject, column string) string {
	table := object.Catalog.Name
	if table == "" {
		table = string(object.Catalog.ID)
	}
	if object.Catalog.Schema != "" {
		table = object.Catalog.Schema + "." + table
	}
	return table + "." + column
}

// compactTargetOmissions resolves the other table's omissions, so a
// declaration reaching across tables leaves out exactly what that table's own
// file leaves out. Its warnings are dropped because that file reports them, and
// repeating them once per relationship would say the same thing several times.
func compactTargetOmissions(ref CompactObjectRef, object CompactObject) compactOmissions {
	accessor := ref.Generation.Source
	if accessor == "" {
		accessor = exportedCompact(ref.Catalog.Name)
	}
	omissions, _ := resolveCompactColumns(compactTargetObject(ref, object), accessor)
	return omissions
}

// compactColumnSelector spells a reference to one column of a generated table,
// reaching through the embedded expressions struct rather than the table value
// itself. A column named like one of the table's own methods, "ref" say, is a
// legal field, but a selector written straight against the table finds the
// method first, because promotion prefers the shallower name. Naming the
// embedded struct skips the method and reaches the field, which is also the
// spelling the documentation gives callers.
func compactColumnSelector(source, accessor, field string) string {
	return source + "." + accessor + "Expressions." + field
}
