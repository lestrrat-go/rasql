package catalog_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestDriftPairsRenamedTables pins the rule tableRenamePairs owns: one table
// left over on each side, equal in everything but its identity, is one
// renamed table rather than an unrelated drop and create.
func TestDriftPairsRenamedTables(t *testing.T) {
	described := []schema.TableDef{tbl("users", "id", "email")}
	live := []schema.TableDef{tbl("members", "id", "email")}

	report := catalog.Drift(described, live)

	require.False(t, report.Empty())
	require.Len(t, report.Renamed(), 1)
	require.Equal(t, "users", report.Renamed()[0].From())
	require.Equal(t, "members", report.Renamed()[0].To())

	// The whole point of the pairing is that neither side reports the table
	// as gone or as new.
	require.Empty(t, report.Added())
	require.Empty(t, report.Removed())
	require.Empty(t, report.Changed())
}

// TestDriftRenameCarriesBothDescriptors pins that a rename hands back the two
// descriptors it paired, as the caller supplied them.
func TestDriftRenameCarriesBothDescriptors(t *testing.T) {
	usersTable := tbl("users", "id")
	membersTable := tbl("members", "id")

	report := catalog.Drift([]schema.TableDef{usersTable}, []schema.TableDef{membersTable})

	require.Len(t, report.Renamed(), 1)
	require.Equal(t, usersTable, report.Renamed()[0].Described())
	require.Equal(t, membersTable, report.Renamed()[0].Live())
}

// TestDriftRefusesToGuessAmbiguousRenames pins the guard that keeps a wrong
// rename from carrying one table's rows under another table's name: a shape
// carried by more than one leftover on either side pairs nothing at all, and
// every table involved is reported as plainly added or removed instead.
func TestDriftRefusesToGuessAmbiguousRenames(t *testing.T) {
	t.Run("two of the shape on each side", func(t *testing.T) {
		described := []schema.TableDef{tbl("colors", "id", "name"), tbl("sizes", "id", "name")}
		live := []schema.TableDef{tbl("hues", "id", "name"), tbl("scales", "id", "name")}

		report := catalog.Drift(described, live)

		require.Empty(t, report.Renamed())
		require.Len(t, report.Added(), 2)
		require.Len(t, report.Removed(), 2)
	})

	t.Run("two removed and one added", func(t *testing.T) {
		described := []schema.TableDef{tbl("colors", "id", "name"), tbl("sizes", "id", "name")}
		live := []schema.TableDef{tbl("hues", "id", "name")}

		report := catalog.Drift(described, live)

		require.Empty(t, report.Renamed())
		require.Len(t, report.Added(), 1)
		require.Len(t, report.Removed(), 2)
	})

	t.Run("one removed and two added", func(t *testing.T) {
		described := []schema.TableDef{tbl("colors", "id", "name")}
		live := []schema.TableDef{tbl("hues", "id", "name"), tbl("scales", "id", "name")}

		report := catalog.Drift(described, live)

		require.Empty(t, report.Renamed())
		require.Len(t, report.Added(), 2)
		require.Len(t, report.Removed(), 1)
	})
}

// TestDriftPairsOneRenameBesideAnAmbiguousShape pins that an ambiguous shape
// blocks only its own pairing. A second shape carried by exactly one leftover
// on each side still pairs.
func TestDriftPairsOneRenameBesideAnAmbiguousShape(t *testing.T) {
	described := []schema.TableDef{
		tbl("colors", "id", "name"),
		tbl("sizes", "id", "name"),
		tbl("users", "id", "email", "phone"),
	}
	live := []schema.TableDef{
		tbl("hues", "id", "name"),
		tbl("scales", "id", "name"),
		tbl("members", "id", "email", "phone"),
	}

	report := catalog.Drift(described, live)

	require.Len(t, report.Renamed(), 1)
	require.Equal(t, "users", report.Renamed()[0].From())
	require.Equal(t, "members", report.Renamed()[0].To())
	require.Len(t, report.Added(), 2)
	require.Len(t, report.Removed(), 2)
}

// TestDriftDoesNotPairTablesOfDifferentShapes pins that the pairing needs
// every fact but the identity to be equal. One differing column is enough to
// keep two leftovers apart.
func TestDriftDoesNotPairTablesOfDifferentShapes(t *testing.T) {
	described := []schema.TableDef{tbl("users", "id", "email")}
	live := []schema.TableDef{tbl("members", "id", "phone")}

	report := catalog.Drift(described, live)

	require.Empty(t, report.Renamed())
	require.Len(t, report.Added(), 1)
	require.Len(t, report.Removed(), 1)
}

// TestDriftDoesNotPairATableRecreatedUnderItsOwnConstraintNames pins the
// accuracy the fingerprint gets for free. PostgreSQL and MySQL both leave a
// renamed table's constraint names alone, so a real rename keeps them; a table
// newly created in the shape of a dropped one carries constraint names derived
// from its own name instead, which is a difference the pairing sees.
func TestDriftDoesNotPairATableRecreatedUnderItsOwnConstraintNames(t *testing.T) {
	usersTable := tbl("users", "id", "email")
	usersTable.UniqueConstraints = []schema.UniqueDef{{Name: "users_email_key", Columns: []string{"email"}}}
	membersTable := tbl("members", "id", "email")
	membersTable.UniqueConstraints = []schema.UniqueDef{{Name: "members_email_key", Columns: []string{"email"}}}

	report := catalog.Drift([]schema.TableDef{usersTable}, []schema.TableDef{membersTable})

	require.Empty(t, report.Renamed())
	require.Len(t, report.Added(), 1)
	require.Len(t, report.Removed(), 1)
}

// TestDriftPairsARenameThatKeptItsConstraintNames is the other half of the
// pair above: the same rename with the constraint name left alone, which is
// what a live catalog reports after ALTER TABLE ... RENAME TO.
func TestDriftPairsARenameThatKeptItsConstraintNames(t *testing.T) {
	usersTable := tbl("users", "id", "email")
	usersTable.UniqueConstraints = []schema.UniqueDef{{Name: "users_email_key", Columns: []string{"email"}}}
	membersTable := tbl("members", "id", "email")
	membersTable.UniqueConstraints = []schema.UniqueDef{{Name: "users_email_key", Columns: []string{"email"}}}

	report := catalog.Drift([]schema.TableDef{usersTable}, []schema.TableDef{membersTable})

	require.Len(t, report.Renamed(), 1)
	require.Equal(t, "users", report.Renamed()[0].From())
	require.Equal(t, "members", report.Renamed()[0].To())
}

// TestDriftPairsATableMovedBetweenSchemas pins that the identity the pairing
// sets aside is the whole qualified name, so a table moved to another schema
// under the same name pairs too. The two qualified names say which of the two
// changes it was.
func TestDriftPairsATableMovedBetweenSchemas(t *testing.T) {
	described := []schema.TableDef{{Schema: "public", Name: "events", Columns: []schema.ColumnDef{col("id")}}}
	live := []schema.TableDef{{Schema: "audit", Name: "events", Columns: []schema.ColumnDef{col("id")}}}

	report := catalog.Drift(described, live)

	require.Len(t, report.Renamed(), 1)
	require.Equal(t, "public.events", report.Renamed()[0].From())
	require.Equal(t, "audit.events", report.Renamed()[0].To())
}

// TestDriftSortsRenamesByTheNameTheyCameFrom pins the order Renamed reports,
// which Drift's own doc promises.
func TestDriftSortsRenamesByTheNameTheyCameFrom(t *testing.T) {
	described := []schema.TableDef{tbl("zebras", "id", "stripes"), tbl("apples", "id", "core")}
	live := []schema.TableDef{tbl("horses", "id", "stripes"), tbl("pears", "id", "core")}

	report := catalog.Drift(described, live)

	require.Len(t, report.Renamed(), 2)
	require.Equal(t, "apples", report.Renamed()[0].From())
	require.Equal(t, "zebras", report.Renamed()[1].From())
}

// TestReportRendersARename pins the rename line Report.String writes, beside
// the added and removed lines it already wrote.
func TestReportRendersARename(t *testing.T) {
	described := []schema.TableDef{tbl("users", "id", "email"), tbl("legacy", "id", "note")}
	live := []schema.TableDef{tbl("members", "id", "email"), tbl("orders", "id", "total")}

	report := catalog.Drift(described, live)

	const expected = `+ table "orders"
- table "legacy"
> table "users" renamed to "members"
`
	require.Equal(t, expected, report.String())
}

// TestRenamedHandsOutCopies pins that a caller cannot reach into a Report by
// mutating what Renamed returned, on the same terms as the other accessors.
func TestRenamedHandsOutCopies(t *testing.T) {
	report := catalog.Drift(
		[]schema.TableDef{tbl("users", "id", "email")},
		[]schema.TableDef{tbl("members", "id", "email")},
	)

	renames := report.Renamed()
	require.Len(t, renames, 1)
	described := renames[0].Described()
	described.Columns[0].Name = "mutated"
	renames[0] = catalog.TableRename{}

	require.Len(t, report.Renamed(), 1)
	require.Equal(t, "users", report.Renamed()[0].From())
	require.Equal(t, "id", report.Renamed()[0].Described().Columns[0].Name)
}
