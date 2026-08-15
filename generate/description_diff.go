package generate

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
)

// DescriptionDiff is the drift `rasqlgen bootstrap` finds, on a refresh,
// between a description package's existing Tables() and a fresh inspection
// of the live database: tables added, tables removed, and tables present on
// both sides whose descriptor changed. DiffDescriptionPackage builds it;
// Report renders it for a person, and Empty reports whether there is
// nothing to show at all.
type DescriptionDiff struct {
	// Added lists tables the live database has that the existing package
	// does not, sorted by name.
	Added []schema.TableDef
	// Removed lists tables the existing package describes that the live
	// database no longer has, sorted by name.
	Removed []schema.TableDef
	// Changed lists tables present on both sides whose descriptor differs,
	// sorted by name.
	Changed []TableDescriptionDiff
}

// TableDescriptionDiff is what changed within one table DiffDescriptionPackage
// found on both sides.
type TableDescriptionDiff struct {
	// Name is the table's name, shared by both sides of the comparison.
	Name string
	// New is the freshly inspected descriptor: what a -write refresh
	// writes in place of the existing file.
	New schema.TableDef
	// Notes is one line per changed fact, already formatted and ordered
	// for Report to indent and print directly.
	Notes []string
}

// Empty reports whether d describes no drift at all: every -dsn table
// still matches -output's existing description exactly, RowName hints
// aside (see DiffDescriptionPackage).
func (d DescriptionDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// Report renders d for a person: one line per added or removed table, and
// one line per changed table followed by its indented notes. Two runs
// against an unchanged database produce equal, empty Reports, so a caller
// can use Report's emptiness as a stable drift check.
func (d DescriptionDiff) Report() string {
	if d.Empty() {
		return ""
	}
	var lines []string
	for _, table := range d.Added {
		lines = append(lines, fmt.Sprintf("+ table %q", table.Name))
	}
	for _, table := range d.Removed {
		lines = append(lines, fmt.Sprintf("- table %q", table.Name))
	}
	for _, changed := range d.Changed {
		lines = append(lines, fmt.Sprintf("~ table %q", changed.Name))
		for _, note := range changed.Notes {
			lines = append(lines, "    "+note)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// DiffDescriptionPackage compares old, the tables an existing description
// package's Tables() returned, against new, a fresh inspection of the live
// database, and returns every difference between them. Both are compared
// with every stated RowName cleared and every unset list field normalized
// to empty first, so a RowNamed hint applied by Tables() -- never
// something a database has an opinion about -- never itself shows up as
// drift, and an unset-vs-empty-slice asymmetry between a struct literal
// read back from Go source and one inspection just built never does
// either.
//
// New tables and changed tables in the result carry the freshly inspected
// descriptor exactly as inspection returned it, RowName included -- which
// inspection always leaves empty, since a database never states one. A
// RowName hint is never written into a per-table file; it lives only in
// hints.go's Hints map and is applied by the aggregator's own Tables()
// function every time it runs, which is what makes it survive a refresh
// that rewrites every table file untouched by hints at all. See
// TableDescriptionDiff.New.
func DiffDescriptionPackage(old, new []schema.TableDef) DescriptionDiff {
	oldByName := make(map[string]schema.TableDef, len(old))
	for _, table := range old {
		oldByName[table.Name] = table
	}
	newByName := make(map[string]schema.TableDef, len(new))
	for _, table := range new {
		newByName[table.Name] = table
	}

	var diff DescriptionDiff
	for _, name := range sortedKeys(newByName) {
		if _, exists := oldByName[name]; !exists {
			diff.Added = append(diff.Added, newByName[name])
		}
	}
	for _, name := range sortedKeys(oldByName) {
		if _, exists := newByName[name]; !exists {
			diff.Removed = append(diff.Removed, oldByName[name])
		}
	}
	for _, name := range sortedKeys(newByName) {
		oldTable, existed := oldByName[name]
		if !existed {
			continue
		}
		newTable := newByName[name]
		notes := diffTable(oldTable, newTable)
		if len(notes) == 0 {
			continue
		}
		diff.Changed = append(diff.Changed, TableDescriptionDiff{Name: name, New: newTable, Notes: notes})
	}
	return diff
}

// sortedKeys returns m's keys in ascending order, giving DiffDescriptionPackage
// and Report the stable ordering an unchanged-database run needs to print
// nothing twice in a row.
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// diffTable returns the ordered, formatted notes describing every way new
// differs from old, after both are normalized by normalizeForDiff. An empty
// result means the two tables are identical for refresh purposes.
func diffTable(old, new schema.TableDef) []string {
	old = normalizeForDiff(old)
	new = normalizeForDiff(new)

	var notes []string
	notes = append(notes, diffColumns(old.Columns, new.Columns)...)
	notes = append(notes, diffNamed(old.Indexes, new.Indexes, "index", func(i schema.IndexDef) string { return i.Name }, describeIndexChange)...)
	notes = append(notes, diffNamed(old.UniqueConstraints, new.UniqueConstraints, "unique constraint", func(u schema.UniqueDef) string { return u.Name }, nil)...)
	notes = append(notes, diffNamed(old.ForeignKeys, new.ForeignKeys, "foreign key", func(f schema.ForeignKeyDef) string { return f.Name }, nil)...)
	notes = append(notes, diffNamed(old.Checks, new.Checks, "check", func(c schema.CheckDef) string { return c.Name }, nil)...)
	notes = append(notes, diffNamed(old.ExclusionConstraints, new.ExclusionConstraints, "exclusion constraint", func(e schema.ExclusionDef) string { return e.Name }, nil)...)
	if !reflect.DeepEqual(old.PrimaryKey, new.PrimaryKey) {
		notes = append(notes, fmt.Sprintf("~ primary key (%s -> %s)", strings.Join(old.PrimaryKey, ", "), strings.Join(new.PrimaryKey, ", ")))
	}
	notes = append(notes, diffTableFacts(old, new)...)
	return notes
}

// diffTableFacts compares the table-level facts diffTable does not already
// cover through a dedicated section: the namespace and the SQLite-only
// table options. RowName is deliberately absent -- normalizeForDiff already
// cleared it on both sides before this runs, since it is a hint, not
// something the database states.
func diffTableFacts(old, new schema.TableDef) []string {
	var notes []string
	if old.Schema != new.Schema {
		notes = append(notes, fmt.Sprintf("~ schema (%q -> %q)", old.Schema, new.Schema))
	}
	if old.Strict != new.Strict {
		notes = append(notes, fmt.Sprintf("~ strict (%t -> %t)", old.Strict, new.Strict))
	}
	if old.WithoutRowID != new.WithoutRowID {
		notes = append(notes, fmt.Sprintf("~ without rowid (%t -> %t)", old.WithoutRowID, new.WithoutRowID))
	}
	if old.PrimaryKeyAutoincrement != new.PrimaryKeyAutoincrement {
		notes = append(notes, fmt.Sprintf("~ primary key autoincrement (%t -> %t)", old.PrimaryKeyAutoincrement, new.PrimaryKeyAutoincrement))
	}
	if old.PrimaryKeyOnConflict != new.PrimaryKeyOnConflict {
		notes = append(notes, fmt.Sprintf("~ primary key on conflict (%q -> %q)", old.PrimaryKeyOnConflict, new.PrimaryKeyOnConflict))
	}
	return notes
}

// diffColumns returns one note per column added, removed, or changed,
// ordered added-then-removed-then-changed, each group sorted by column
// name.
func diffColumns(old, new []schema.ColumnDef) []string {
	oldByName := make(map[string]schema.ColumnDef, len(old))
	for _, column := range old {
		oldByName[column.Name] = column
	}
	newByName := make(map[string]schema.ColumnDef, len(new))
	for _, column := range new {
		newByName[column.Name] = column
	}

	var added, removed, changed []string
	for _, name := range sortedKeys(newByName) {
		if _, exists := oldByName[name]; !exists {
			added = append(added, fmt.Sprintf("+ column %q", name))
		}
	}
	for _, name := range sortedKeys(oldByName) {
		if _, exists := newByName[name]; !exists {
			removed = append(removed, fmt.Sprintf("- column %q", name))
		}
	}
	for _, name := range sortedKeys(newByName) {
		oldColumn, existed := oldByName[name]
		if !existed {
			continue
		}
		newColumn := newByName[name]
		if fact := describeColumnChange(oldColumn, newColumn); fact != "" {
			changed = append(changed, fact)
		}
	}
	notes := make([]string, 0, len(added)+len(removed)+len(changed))
	notes = append(notes, added...)
	notes = append(notes, removed...)
	notes = append(notes, changed...)
	return notes
}

// describeColumnChange returns a formatted note describing how new differs
// from old, or an empty string when they are equal. Every column fact is
// checked explicitly, rather than falling back to a generic "changed" note
// on any reflect.DeepEqual mismatch, so the note always says which facts
// actually moved.
func describeColumnChange(old, new schema.ColumnDef) string {
	var facts []string
	if !reflect.DeepEqual(old.Type, new.Type) {
		if old.Type != nil && new.Type != nil && old.Type.Kind() == new.Type.Kind() {
			facts = append(facts, fmt.Sprintf("type detail (%s)", old.Type.Kind()))
		} else {
			facts = append(facts, fmt.Sprintf("type (%s -> %s)", columnTypeLabel(old.Type), columnTypeLabel(new.Type)))
		}
	}
	if old.Nullable != new.Nullable {
		facts = append(facts, fmt.Sprintf("nullable (%t -> %t)", old.Nullable, new.Nullable))
	}
	if old.Default != new.Default {
		facts = append(facts, fmt.Sprintf("default (%q -> %q)", old.Default, new.Default))
	}
	if old.GeneratedExpression != new.GeneratedExpression {
		facts = append(facts, fmt.Sprintf("generated expression (%q -> %q)", old.GeneratedExpression, new.GeneratedExpression))
	}
	if old.GeneratedStorage != new.GeneratedStorage {
		facts = append(facts, fmt.Sprintf("generated storage (%q -> %q)", old.GeneratedStorage, new.GeneratedStorage))
	}
	if len(facts) == 0 {
		return ""
	}
	return fmt.Sprintf("~ column %q: %s", new.Name, strings.Join(facts, "; "))
}

// columnTypeLabel returns t's kind, or "none" for a nil ColumnType, which
// TableDef.Validate never actually allows but describeColumnChange still
// guards against rather than panicking on a malformed input.
func columnTypeLabel(t schema.ColumnType) string {
	if t == nil {
		return "none"
	}
	return string(t.Kind())
}

// describeIndexChange reports the one IndexDef fact diffTable's generic
// diffNamed call cannot see on its own: Unique, since two differently-keyed
// indexes of the same name are already unusual enough that naming every
// other differing field would add noise without adding clarity. Any other
// difference still marks the index changed; it is just not itemized.
func describeIndexChange(old, new schema.IndexDef) string {
	if old.Unique != new.Unique {
		return fmt.Sprintf("unique (%t -> %t)", old.Unique, new.Unique)
	}
	return ""
}

// diffNamed returns one note per T added, removed, or changed between old
// and new, keyed by name(T), ordered added-then-removed-then-changed, each
// group sorted by name. describeChange, when non-nil, is tried first for a
// changed entry's note; when it returns "", or is nil, the note names only
// that the entry changed, without itemizing which field.
func diffNamed[T any](old, new []T, label string, name func(T) string, describeChange func(T, T) string) []string {
	oldByName := make(map[string]T, len(old))
	for _, value := range old {
		oldByName[name(value)] = value
	}
	newByName := make(map[string]T, len(new))
	for _, value := range new {
		newByName[name(value)] = value
	}

	var added, removed, changed []string
	for _, key := range sortedKeys(oldByName) {
		if _, exists := newByName[key]; !exists {
			removed = append(removed, fmt.Sprintf("- %s %q", label, key))
		}
	}
	for _, key := range sortedKeys(newByName) {
		oldValue, existed := oldByName[key]
		if !existed {
			added = append(added, fmt.Sprintf("+ %s %q", label, key))
			continue
		}
		newValue := newByName[key]
		if reflect.DeepEqual(oldValue, newValue) {
			continue
		}
		detail := ""
		if describeChange != nil {
			detail = describeChange(oldValue, newValue)
		}
		if detail == "" {
			changed = append(changed, fmt.Sprintf("~ %s %q", label, key))
			continue
		}
		changed = append(changed, fmt.Sprintf("~ %s %q: %s", label, key, detail))
	}
	notes := make([]string, 0, len(added)+len(removed)+len(changed))
	notes = append(notes, added...)
	notes = append(notes, removed...)
	notes = append(notes, changed...)
	return notes
}

// normalizeForDiff returns a copy of table suitable for structural
// comparison: RowName cleared, since it is a hint Tables() applies rather
// than something the database states, and every list the assignments below
// name -- the table's own PrimaryKey, and the column and element lists of
// each unique constraint, exclusion constraint, index and foreign key --
// normalized so an unset (nil) slice compares equal to a stated-but-empty
// one. Every other container is compared exactly as the descriptor states
// it. Without this, a table read back from a hand-written struct literal
// (which the encoder omits a zero-length field from entirely, leaving it
// nil once compiled) would spuriously differ from the same table fresh out
// of inspection, which does not always leave the same field nil.
//
// The table's own constraint, index and column lists need no such
// normalization: diffColumns and diffNamed key each of them by element name
// and compare the elements, so an empty list and an unset one both yield no
// elements and read identically. TableDef.Clone preserves each field's
// nilness rather than folding an empty list to nil, so nothing here may
// rely on it to erase the difference.
func normalizeForDiff(table schema.TableDef) schema.TableDef {
	table = table.Clone()
	table.RowName = ""
	table.Relationships = nil
	if table.PrimaryKey == nil {
		table.PrimaryKey = []string{}
	}
	for index, unique := range table.UniqueConstraints {
		if unique.Columns == nil {
			table.UniqueConstraints[index].Columns = []string{}
		}
	}
	for index, exclusion := range table.ExclusionConstraints {
		if exclusion.Elements == nil {
			table.ExclusionConstraints[index].Elements = []schema.ExclusionElementDef{}
		}
	}
	for index, idx := range table.Indexes {
		if idx.Columns == nil {
			table.Indexes[index].Columns = []string{}
		}
		if idx.Expressions == nil {
			table.Indexes[index].Expressions = []string{}
		}
	}
	for index, key := range table.ForeignKeys {
		if key.Columns == nil {
			table.ForeignKeys[index].Columns = []string{}
		}
		if key.ReferencedColumns == nil {
			table.ForeignKeys[index].ReferencedColumns = []string{}
		}
	}
	return table
}
