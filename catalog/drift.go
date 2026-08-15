package catalog

import (
	"reflect"
	"sort"

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
//		log.Print(report)
//	}
//
// Tables are matched by QualifiedName. A table in live that described does
// not have is Added, a table in described that live does not have is
// Removed, and a table in both whose descriptors differ is Changed. Both
// slices may be empty or nil; comparing two empty slices reports no drift.
//
// A table is Removed whenever described has it and live does not, whatever
// the reason -- including a live side deliberately narrowed with
// Options.Include, which is the one way to ask a question this function will
// answer literally rather than helpfully. A caller comparing against a
// narrowed sweep should read Added and Changed and ignore Removed; nothing
// here can distinguish a table that was dropped from a table that was not
// swept, and guessing would mean sometimes missing a real drop.
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

	var report Report
	for _, oi := range m.removed {
		report.removed = append(report.removed, described[oi].Clone())
	}
	for _, ni := range m.added {
		report.added = append(report.added, live[ni].Clone())
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
// A Report holds copies of the descriptors it was given. Its accessors hand
// out further copies, so nothing a caller does to the result can reach back
// into the Report or into the slices Drift was called with.
type Report struct {
	added   []schema.TableDef
	removed []schema.TableDef
	changed []TableDrift
}

// Empty reports whether the comparison found nothing at all: no table added,
// none removed, and none changed. Two comparisons against an unchanged
// database both report true, which is what makes this safe to run on a
// schedule as an alarm condition.
func (r Report) Empty() bool {
	return len(r.added) == 0 && len(r.removed) == 0 && len(r.changed) == 0
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

// Changed reports every table both sides have whose descriptors differ,
// sorted by QualifiedName.
func (r Report) Changed() []TableDrift {
	if r.changed == nil {
		return nil
	}
	return append([]TableDrift(nil), r.changed...)
}

// cloneTableDefs returns a copy of tables in which every element is itself a
// clone, so neither the slice header nor any container reachable from an
// element is shared with tables.
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

// TableDrift is one table both sides have, and every way the two descriptors
// differ.
//
// Changes is an explanation, not the finding. Whether this table drifted was
// decided by comparing the two whole descriptors, so a fact no change line
// knows how to describe still put the table here; see Changes.
type TableDrift struct {
	described schema.TableDef
	live      schema.TableDef
}

// newTableDrift builds a TableDrift from the caller's own described and live
// descriptors, cloning each so the result shares no container with either
// argument.
func newTableDrift(described, live schema.TableDef) TableDrift {
	return TableDrift{described: described.Clone(), live: live.Clone()}
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
