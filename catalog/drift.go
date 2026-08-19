package catalog

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
)

// Drift compares described, the descriptors an application was generated
// from, against live, the descriptors a database reports now, and returns a
// Report the caller inspects.
//
// The usual described side is a generated store's own Tables(), and the usual
// live side is what FromDatabase just read:
//
//	live, err := catalog.FromDatabase(ctx, database, options)
//	if err != nil {
//		return err
//	}
//	if report := catalog.Drift(store.Tables(), live); !report.Empty() {
//		log.Printf("schema drift: %d table(s) added, %d removed, %d renamed, %d changed",
//			len(report.Added()), len(report.Removed()), len(report.Renamed()), len(report.Changed()))
//	}
//
// Tables are matched by QualifiedName. A table in live that described does
// not have is Added, a table in described that live does not have is
// Removed, and a table in both whose descriptors differ is Changed. Both
// slices may be empty or nil; comparing two empty slices reports no drift.
//
// One table left over on each side is Renamed instead, rather than Added and
// Removed separately, when the two descriptors are equal in everything but
// their Schema and Name. That pairing is a judgment about what happened, and
// it is made only where there is nothing to guess: exactly one leftover table
// on each side may carry the shape. tableRenamePairs owns the rule and the
// case it refuses.
//
// A table is Removed whenever described has it, live does not, and no rename
// paired it, whatever the reason -- including a live side deliberately
// narrowed with Options.Include, which is the one way to ask a question this
// function will answer literally rather than helpfully. A caller comparing
// against a narrowed sweep should read Added and Changed and ignore Removed;
// nothing here can distinguish a table that was dropped from a table that was
// not swept, and guessing would mean sometimes missing a real drop. A
// narrowed sweep cannot turn into a false rename, since narrowing only takes
// tables off the live side and a rename needs a leftover on both.
//
// Two facts are ignored on both sides, because the generator states them and
// no database can: TableDef.RowName, which inspect never sets, and
// TableDef.Relationships, which a generated descriptor derives from its own
// foreign keys. A zero-length slice or map is treated as unset everywhere,
// at every depth, because the generated descriptor cannot express the
// difference. Nothing else is normalized: element order is compared, since a
// reordered list is a list that would regenerate differently.
//
// Drift reads nothing, writes nothing, and modifies neither argument.
func Drift(described, live []schema.TableDef) Report {
	describedNorm := make([]schema.TableDef, len(described))
	for i, table := range described {
		describedNorm[i] = normalize(table)
	}
	liveNorm := make([]schema.TableDef, len(live))
	for i, table := range live {
		liveNorm[i] = normalize(table)
	}

	m := match(describedNorm, liveNorm)
	renames := tableRenamePairs(m.removed, m.added, describedNorm, liveNorm)
	renamedOld := make(map[int]struct{}, len(renames))
	renamedNew := make(map[int]struct{}, len(renames))
	for _, p := range renames {
		renamedOld[p.oldIndex] = struct{}{}
		renamedNew[p.newIndex] = struct{}{}
	}

	var report Report
	for _, oi := range m.removed {
		if _, renamed := renamedOld[oi]; renamed {
			continue
		}
		report.removed = append(report.removed, described[oi].Clone())
	}
	for _, ni := range m.added {
		if _, renamed := renamedNew[ni]; renamed {
			continue
		}
		report.added = append(report.added, live[ni].Clone())
	}
	for _, p := range renames {
		report.renamed = append(report.renamed, TableRename{
			described: described[p.oldIndex].Clone(),
			live:      live[p.newIndex].Clone(),
		})
	}
	for _, p := range m.pairs {
		if reflect.DeepEqual(describedNorm[p.oldIndex], liveNorm[p.newIndex]) {
			continue
		}
		report.changed = append(report.changed, newTableDrift(described[p.oldIndex], live[p.newIndex]))
	}

	sort.Slice(report.added, func(i, j int) bool {
		return report.added[i].QualifiedName() < report.added[j].QualifiedName()
	})
	sort.Slice(report.removed, func(i, j int) bool {
		return report.removed[i].QualifiedName() < report.removed[j].QualifiedName()
	})
	sort.Slice(report.renamed, func(i, j int) bool {
		return report.renamed[i].From() < report.renamed[j].From()
	})
	sort.Slice(report.changed, func(i, j int) bool {
		return report.changed[i].QualifiedName() < report.changed[j].QualifiedName()
	})
	return report
}

// Report is the result of one comparison: which tables the database has that
// the application does not describe, which it describes that the database
// does not have, and, for a table both sides have, exactly how the two
// descriptors differ.
//
// The zero Report describes no drift at all: Empty reports true, every
// bucket is empty, and String returns "".
//
// A Report is a verdict first and a rendering second: the three accessors
// hand out the descriptors it holds, and String renders those same buckets
// for a person to read.
//
// A Report holds copies of the descriptors it was given. Its accessors hand
// out further copies, so nothing a caller does to the result can reach back
// into the Report or into the slices Drift was called with.
type Report struct {
	added   []schema.TableDef
	removed []schema.TableDef
	renamed []TableRename
	changed []TableDrift
}

// Empty reports whether the comparison found nothing at all: no table added,
// none removed, none renamed, and none changed. Two comparisons against an
// unchanged database both report true, which is what makes this safe to run
// on a schedule as an alarm condition.
func (r Report) Empty() bool {
	return len(r.added) == 0 && len(r.removed) == 0 && len(r.renamed) == 0 && len(r.changed) == 0
}

// Added reports every table the database has that the application does not
// describe, as the database described it, sorted by QualifiedName.
func (r Report) Added() []schema.TableDef {
	return cloneTableDefs(r.added)
}

// Removed reports every table the application describes that the database
// does not have, as the application described it, sorted by QualifiedName.
// See Drift's own doc for the one case in which a table appears here without
// having been dropped.
func (r Report) Removed() []schema.TableDef {
	return cloneTableDefs(r.removed)
}

// Renamed reports every table the comparison paired across a name change,
// sorted by the name the application describes. A renamed table appears here
// and in neither Added nor Removed.
func (r Report) Renamed() []TableRename {
	if r.renamed == nil {
		return nil
	}
	return append([]TableRename(nil), r.renamed...)
}

// Changed reports every table both sides have whose descriptors differ,
// sorted by QualifiedName.
func (r Report) Changed() []TableDrift {
	if r.changed == nil {
		return nil
	}
	return append([]TableDrift(nil), r.changed...)
}

// String renders the report for a person: one line per added or removed
// table, and one line per changed table followed by its own indented change
// lines. It returns "" for an empty Report, and otherwise a block of
// newline-terminated lines, so a caller can print it unconditionally and
// print nothing when there is nothing to say.
//
// The format is stable and deterministic: two comparisons of equal inputs
// render byte-identical text.
func (r Report) String() string {
	if r.Empty() {
		return ""
	}
	var b strings.Builder
	for _, table := range r.added {
		fmt.Fprintf(&b, "+ table %q\n", table.QualifiedName())
	}
	for _, table := range r.removed {
		fmt.Fprintf(&b, "- table %q\n", table.QualifiedName())
	}
	for _, rename := range r.renamed {
		b.WriteString(rename.String())
	}
	for _, drift := range r.changed {
		b.WriteString(drift.String())
	}
	return b.String()
}

// cloneTableDefs returns a copy of tables in which every element is itself a
// clone, so neither the slice header nor anything reachable from an element
// is shared with tables. TableDef.Clone owns what "anything" covers: every
// container at every depth, and the value a column type held by pointer
// points at.
func cloneTableDefs(tables []schema.TableDef) []schema.TableDef {
	if tables == nil {
		return nil
	}
	clone := make([]schema.TableDef, len(tables))
	for i, table := range tables {
		clone[i] = table.Clone()
	}
	return clone
}

// TableRename is one table the comparison found under two names: a table the
// application describes, and a table the database reports whose descriptor is
// equal to it in everything but its identity.
//
// A TableRename carries no change list, because there is nothing to list. The
// pair was made only because the two descriptors are equal once their Schema
// and Name are set aside, so the name is the whole difference.
//
// The pairing is a judgment, not a fact a database reports. See
// tableRenamePairs for the one rule that makes it and the case it refuses to
// guess at.
type TableRename struct {
	described schema.TableDef
	live      schema.TableDef
}

// From returns the qualified name the application describes, the name the
// table is being renamed away from. It is for display only and is never a SQL
// identifier.
func (r TableRename) From() string {
	return r.described.QualifiedName()
}

// To returns the qualified name the database reports, the name the table is
// being renamed to. It is for display only and is never a SQL identifier.
func (r TableRename) To() string {
	return r.live.QualifiedName()
}

// Described returns the descriptor the application was generated from,
// exactly as the caller supplied it.
func (r TableRename) Described() schema.TableDef {
	return r.described.Clone()
}

// Live returns the descriptor the database reports now, exactly as the caller
// supplied it.
func (r TableRename) Live() schema.TableDef {
	return r.live.Clone()
}

// String renders this rename as one newline-terminated line.
func (r TableRename) String() string {
	return fmt.Sprintf("> table %q renamed to %q\n", r.From(), r.To())
}

// TableDrift is one table both sides have, and every way the two descriptors
// differ.
//
// Changes is an explanation, not the finding. Whether this table drifted was
// decided by comparing the two whole descriptors, so a fact no change line
// knows how to describe still put the table here; see Changes.
type TableDrift struct {
	described schema.TableDef
	live      schema.TableDef
	changes   []Change
}

// newTableDrift builds a TableDrift from the caller's own described and live
// descriptors, cloning each so the result shares nothing with either
// argument, on the same terms as cloneTableDefs. Its change list is built
// from the two normalized descriptors -- the same values the verdict in
// Drift already found unequal -- so the explanation can never describe a
// fact the verdict itself ignored (§2.4).
func newTableDrift(described, live schema.TableDef) TableDrift {
	return TableDrift{
		described: described.Clone(),
		live:      live.Clone(),
		changes:   tableChanges(normalize(described), normalize(live)),
	}
}

// Schema returns the namespace the table lives in, empty when the table is
// unqualified. It is the same on both sides, since it is part of the
// identity the two descriptors were matched on.
func (d TableDrift) Schema() string {
	return d.described.Schema
}

// Name returns the table's name, the same on both sides.
func (d TableDrift) Name() string {
	return d.described.Name
}

// QualifiedName returns "schema.name" for a qualified table and "name"
// otherwise: what the report prints and what the two descriptors were
// matched on. It is for display only and is never a SQL identifier.
func (d TableDrift) QualifiedName() string {
	return d.described.QualifiedName()
}

// Described returns the descriptor the application was generated from,
// exactly as the caller supplied it -- the ignored facts are ignored when
// comparing, not stripped from what is handed back.
func (d TableDrift) Described() schema.TableDef {
	return d.described.Clone()
}

// Live returns the descriptor the database reports now, exactly as the
// caller supplied it. A caller that wants to regenerate from the current
// schema already holds it here and need not read the database again.
func (d TableDrift) Live() schema.TableDef {
	return d.live.Clone()
}

// Changes reports every difference between the two descriptors, in a fixed
// order: the table's own facts first, then each list of elements in the
// order schema.TableDef declares them, and within a list every addition,
// then every removal, then every modification, then every move, each group
// sorted by subject.
//
// The result is never empty. A difference the walk cannot itemize is
// reported as one change naming the two whole descriptors rather than as
// silence, so a table reported as drifted always says something about why.
func (d TableDrift) Changes() []Change {
	return cloneChanges(d.changes)
}

// cloneChanges deep-copies changes, including each element's own Fields
// slice, so a caller cannot reach into a TableDrift by mutating what
// Changes returned.
func cloneChanges(changes []Change) []Change {
	if changes == nil {
		return nil
	}
	clone := make([]Change, len(changes))
	for i, change := range changes {
		clone[i] = Change{
			Kind:    change.Kind,
			Subject: change.Subject,
			Fields:  append([]FieldChange(nil), change.Fields...),
		}
	}
	return clone
}

// String renders this table's section of the report: the "~ table" line
// followed by one four-space indented line per change, each newline
// terminated.
func (d TableDrift) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "~ table %q\n", d.QualifiedName())
	for _, change := range d.changes {
		fmt.Fprintf(&b, "    %s\n", change.String())
	}
	return b.String()
}
