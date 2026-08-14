package generate_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestDiffDescriptionPackageEmptyWhenUnchanged pins the property a refresh
// depends on most: comparing a table against an identical copy of itself
// reports no drift at all, so two runs against an unchanged database print
// nothing and exit cleanly.
func TestDiffDescriptionPackageEmptyWhenUnchanged(t *testing.T) {
	users := usersTableDef()
	diff := generate.DiffDescriptionPackage([]schema.TableDef{users}, []schema.TableDef{users})
	require.True(t, diff.Empty())
	require.Equal(t, "", diff.Report())
}

// TestDiffDescriptionPackageIgnoresRowNameHint proves a RowName the
// existing package's Tables() applied from hints.go -- never something the
// database states -- never itself shows up as drift against a fresh
// inspection, which always leaves RowName empty.
func TestDiffDescriptionPackageIgnoresRowNameHint(t *testing.T) {
	users := usersTableDef()
	hinted := users
	hinted.RowName = "User"

	diff := generate.DiffDescriptionPackage([]schema.TableDef{hinted}, []schema.TableDef{users})
	require.True(t, diff.Empty(), "report: %s", diff.Report())
}

func TestDiffDescriptionPackageReportsAddedAndRemovedTables(t *testing.T) {
	users, orders := usersTableDef(), ordersTableDef()
	diff := generate.DiffDescriptionPackage([]schema.TableDef{users}, []schema.TableDef{users, orders})
	require.Equal(t, []schema.TableDef{orders}, diff.Added)
	require.Empty(t, diff.Removed)
	require.Empty(t, diff.Changed)
	require.Equal(t, "+ table \"orders\"\n", diff.Report())

	reverse := generate.DiffDescriptionPackage([]schema.TableDef{users, orders}, []schema.TableDef{users})
	require.Equal(t, []schema.TableDef{orders}, reverse.Removed)
	require.Empty(t, reverse.Added)
	require.Equal(t, "- table \"orders\"\n", reverse.Report())
}

// TestDiffDescriptionPackageReportsColumnChanges covers a column added, a
// column removed, and a column whose nullable fact changed, all on the
// same table, which is what a live sweep's own drift usually looks like.
func TestDiffDescriptionPackageReportsColumnChanges(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("fax"),
		schema.PrimaryKey("id"),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email", schema.Nullable()),
		schema.Text("phone"),
		schema.PrimaryKey("id"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.Len(t, diff.Changed, 1)
	changed := diff.Changed[0]
	require.Equal(t, "users", changed.Name)
	require.Equal(t, after, changed.New)
	require.Contains(t, changed.Notes, `+ column "phone"`)
	require.Contains(t, changed.Notes, `- column "fax"`)
	require.Contains(t, changed.Notes, `~ column "email": nullable (false -> true)`)

	report := diff.Report()
	require.Contains(t, report, `~ table "users"`)
	require.Contains(t, report, "    + column \"phone\"")
}

// TestDiffDescriptionPackageReportsIndexChanges covers an index added, an
// index removed, and an index whose uniqueness changed.
func TestDiffDescriptionPackageReportsIndexChanges(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Index("users_email_idx", "email"),
		schema.Index("users_dropped_idx", "id"),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.UniqueIndex("users_email_idx", "email"),
		schema.Index("users_added_idx", "id"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.Len(t, diff.Changed, 1)
	notes := diff.Changed[0].Notes
	require.Contains(t, notes, `+ index "users_added_idx"`)
	require.Contains(t, notes, `- index "users_dropped_idx"`)
	require.Contains(t, notes, `~ index "users_email_idx": unique (false -> true)`)
}

// TestDiffDescriptionPackageReportsChangedUnnamedCheck is the test that
// fails today: two unnamed checks keyed only by their shared empty Name
// collapse into one map entry, so the first check's own change is
// invisible. old and new both carry a second unnamed check, "id > 0",
// unchanged, which must not appear in either note.
func TestDiffDescriptionPackageReportsChangedUnnamedCheck(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("length(email) > 0"),
		schema.Check("id > 0"),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("length(email) > 5"),
		schema.Check("id > 0"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.Len(t, diff.Changed, 1)
	notes := diff.Changed[0].Notes
	require.Contains(t, notes, "+ unnamed check (length(email) > 5)")
	require.Contains(t, notes, "- unnamed check (length(email) > 0)")
	for _, note := range notes {
		require.NotContains(t, note, "id > 0")
	}
}

// TestDiffDescriptionPackageReportsDroppedUnnamedCheck proves dropping one
// of two unnamed checks is reported, rather than vanishing behind the
// surviving one.
func TestDiffDescriptionPackageReportsDroppedUnnamedCheck(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("length(email) > 0"),
		schema.Check("id > 0"),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("id > 0"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.Len(t, diff.Changed, 1)
	notes := diff.Changed[0].Notes
	require.Equal(t, []string{"- unnamed check (length(email) > 0)"}, notes)
}

// TestDiffDescriptionPackageIgnoresUnnamedConstraintOrder proves an unnamed
// constraint's comparison is order-insensitive, matching what diffNamed
// already does for named entries.
func TestDiffDescriptionPackageIgnoresUnnamedConstraintOrder(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("length(email) > 0"),
		schema.Check("id > 0"),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("id > 0"),
		schema.Check("length(email) > 0"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.True(t, diff.Empty(), "report: %s", diff.Report())
}

// TestDiffDescriptionPackageCountsRepeatedUnnamedConstraints proves the
// comparison counts identical unnamed entries rather than de-duplicating
// them: dropping one of two identical checks is reported once.
func TestDiffDescriptionPackageCountsRepeatedUnnamedConstraints(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("id > 0"),
		schema.Check("id > 0"),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Check("id > 0"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.Len(t, diff.Changed, 1)
	notes := diff.Changed[0].Notes
	require.Equal(t, []string{"- unnamed check (id > 0)"}, notes)
}

// TestDiffDescriptionPackageReportsUnnamedForeignKey proves an unnamed
// foreign key is reported by what it says, matching the check coverage
// above for a different unnamed constraint kind.
func TestDiffDescriptionPackageReportsUnnamedForeignKey(t *testing.T) {
	before := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Integer("user_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("user_id", schema.References("users", "id")),
	)
	after := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Integer("user_id"),
		schema.PrimaryKey("id"),
	)

	diff := generate.DiffDescriptionPackage([]schema.TableDef{before}, []schema.TableDef{after})
	require.Len(t, diff.Changed, 1)
	notes := diff.Changed[0].Notes
	require.Equal(t, []string{"- unnamed foreign key (user_id -> users(id))"}, notes)
}

// TestDiffDescriptionPackageOrderingIsStable proves the report orders
// tables and notes deterministically regardless of input order, which is
// what lets a caller diff two reports across runs.
func TestDiffDescriptionPackageOrderingIsStable(t *testing.T) {
	users, orders := usersTableDef(), ordersTableDef()
	oldTables := []schema.TableDef{orders}
	newTables := []schema.TableDef{orders, users}

	first := generate.DiffDescriptionPackage(oldTables, newTables)
	second := generate.DiffDescriptionPackage(append([]schema.TableDef(nil), oldTables...), append([]schema.TableDef(nil), newTables...))
	require.Equal(t, first.Report(), second.Report())
	require.Equal(t, "+ table \"users\"\n", first.Report())
}
