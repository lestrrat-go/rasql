package catalog_test

import (
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// usersReportFixture returns the described (store) and live (database)
// descriptors for "users" the phase 4 specification's §5 walks through by
// hand: the nickname column dropped, a phone column added, email became
// nullable, the table became STRICT, an unnamed unique constraint's ON
// CONFLICT changed, a named unique constraint's deferrability changed, the
// first of two unnamed checks changed, an index was added, and a named
// foreign key's delete action changed.
func usersReportFixture() (described, live schema.TableDef) {
	described = schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			col("id"),
			{Name: "email", Type: schema.TextType{}, Nullable: false},
			{Name: "nickname", Type: schema.TextType{}},
		},
		UniqueConstraints: []schema.UniqueDef{
			{Columns: []string{"nickname"}, OnConflict: schema.ConflictResolution("IGNORE")},
			{Name: "users_email_key", Columns: []string{"email"}, Deferrable: schema.Deferrability("DEFERRABLE INITIALLY IMMEDIATE")},
		},
		Checks: []schema.CheckDef{
			{Expression: "id > 0"},
			{Expression: "created_at IS NOT NULL"},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name: "users_org_id_fkey", Columns: []string{"org_id"},
				ReferencedTable: "orgs", ReferencedColumns: []string{"id"},
				OnDelete: schema.ReferenceAction("CASCADE"),
			},
		},
	}

	live = schema.TableDef{
		Name:   "users",
		Strict: true,
		Columns: []schema.ColumnDef{
			col("id"),
			{Name: "email", Type: schema.TextType{}, Nullable: true},
			{Name: "phone", Type: schema.TextType{}},
		},
		UniqueConstraints: []schema.UniqueDef{
			{Columns: []string{"nickname"}, OnConflict: schema.ConflictResolution("REPLACE")},
			{Name: "users_email_key", Columns: []string{"email"}, Deferrable: schema.Deferrability("DEFERRABLE INITIALLY DEFERRED")},
		},
		Checks: []schema.CheckDef{
			{Expression: "id >= 0"},
			{Expression: "created_at IS NOT NULL"},
		},
		Indexes: []schema.IndexDef{
			{Name: "users_phone_idx", Columns: []string{"phone"}},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name: "users_org_id_fkey", Columns: []string{"org_id"},
				ReferencedTable: "orgs", ReferencedColumns: []string{"id"},
				OnDelete: schema.ReferenceAction("SET NULL"),
			},
		},
	}
	return described, live
}

// TestReportRendersEveryKindOfChange reproduces the phase 4 specification's
// §5 report, byte for byte: an added table, a removed table, and a changed
// table exercising every shape of change this package renders.
func TestReportRendersEveryKindOfChange(t *testing.T) {
	usersDescribed, usersLive := usersReportFixture()
	legacyUsers := tbl("legacy_users", "id")
	orders := tbl("orders", "id")

	report := catalog.Drift(
		[]schema.TableDef{legacyUsers, usersDescribed},
		[]schema.TableDef{usersLive, orders},
	)

	const expected = `+ table "orders"
- table "legacy_users"
~ table "users"
    ~ Strict: false -> true
    + column "phone"
    - column "nickname"
    ~ column "email": Nullable: false -> true
    + unique constraint {Columns:["nickname"] OnConflict:"REPLACE"}
    - unique constraint {Columns:["nickname"] OnConflict:"IGNORE"}
    ~ unique constraint "users_email_key": Deferrable: "DEFERRABLE INITIALLY IMMEDIATE" -> "DEFERRABLE INITIALLY DEFERRED"
    + check {Expression:"id >= 0"}
    - check {Expression:"id > 0"}
    + index "users_phone_idx"
    ~ foreign key "users_org_id_fkey": OnDelete: "CASCADE" -> "SET NULL"
`
	require.Equal(t, expected, report.String())
}

func TestReportIsEmptyStringWhenThereIsNoDrift(t *testing.T) {
	tables := []schema.TableDef{tbl("users", "id"), tbl("orders", "id")}
	report := catalog.Drift(tables, tables)
	require.True(t, report.Empty())
	require.Equal(t, "", report.String())
}

// TestReportNamesAnUnnamedConstraintByItsWholeRendering pins §2.5 point 3
// and closes the ambiguity parked as pull request 161: a check, a unique
// constraint, and a foreign key that state no name and differ in exactly
// one field each produce exactly one "-" line and one "+" line, the two
// lines are NOT equal to each other, and neither line invents a name or
// mentions an index.
func TestReportNamesAnUnnamedConstraintByItsWholeRendering(t *testing.T) {
	t.Run("check", func(t *testing.T) {
		described := tbl("t", "id")
		described.Checks = []schema.CheckDef{{Expression: "id > 0"}}
		live := tbl("t", "id")
		live.Checks = []schema.CheckDef{{Expression: "id >= 0"}}

		added, removed := onlyAddedAndRemoved(t, described, live)
		require.Equal(t, `check {Expression:"id >= 0"}`, added.Subject)
		require.Equal(t, `check {Expression:"id > 0"}`, removed.Subject)
		require.NotEqual(t, added.Subject, removed.Subject)
		require.NotContains(t, added.Subject, "index")
		require.NotContains(t, removed.Subject, "index")
	})

	t.Run("unique constraint differing only in OnConflict", func(t *testing.T) {
		described := tbl("t", "id")
		described.UniqueConstraints = []schema.UniqueDef{{Columns: []string{"id"}, OnConflict: schema.ConflictResolution("IGNORE")}}
		live := tbl("t", "id")
		live.UniqueConstraints = []schema.UniqueDef{{Columns: []string{"id"}, OnConflict: schema.ConflictResolution("REPLACE")}}

		added, removed := onlyAddedAndRemoved(t, described, live)
		require.Equal(t, `unique constraint {Columns:["id"] OnConflict:"REPLACE"}`, added.Subject)
		require.Equal(t, `unique constraint {Columns:["id"] OnConflict:"IGNORE"}`, removed.Subject)
		require.NotEqual(t, added.Subject, removed.Subject)
	})

	t.Run("foreign key differing only in OnDelete", func(t *testing.T) {
		described := tbl("t", "id")
		described.ForeignKeys = []schema.ForeignKeyDef{{
			Columns: []string{"id"}, ReferencedTable: "other", ReferencedColumns: []string{"id"},
			OnDelete: schema.ReferenceAction("CASCADE"),
		}}
		live := tbl("t", "id")
		live.ForeignKeys = []schema.ForeignKeyDef{{
			Columns: []string{"id"}, ReferencedTable: "other", ReferencedColumns: []string{"id"},
			OnDelete: schema.ReferenceAction("SET NULL"),
		}}

		added, removed := onlyAddedAndRemoved(t, described, live)
		require.Equal(t, `foreign key {Columns:["id"] ReferencedTable:"other" ReferencedColumns:["id"] OnDelete:"SET NULL"}`, added.Subject)
		require.Equal(t, `foreign key {Columns:["id"] ReferencedTable:"other" ReferencedColumns:["id"] OnDelete:"CASCADE"}`, removed.Subject)
		require.NotEqual(t, added.Subject, removed.Subject)
	})
}

// onlyAddedAndRemoved drifts described against live, requires exactly one
// changed table whose only two changes are one addition and one removal (in
// that order, per §4.5), and returns them.
func onlyAddedAndRemoved(t *testing.T, described, live schema.TableDef) (added, removed catalog.Change) {
	t.Helper()
	report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
	require.Len(t, report.Changed(), 1)
	changes := report.Changed()[0].Changes()
	require.Len(t, changes, 2)
	require.Equal(t, catalog.ChangeAdded, changes[0].Kind)
	require.Equal(t, catalog.ChangeRemoved, changes[1].Kind)
	return changes[0], changes[1]
}

// TestReportDetailsEveryNamedConstraintKind pins the case the old
// comparison answered with a bare "~ unique constraint \"u\""
// (generate/description_diff.go:317): a named unique constraint, check,
// index, foreign key, and exclusion constraint each changed in one field
// produce a "~" line naming that field and its two values.
func TestReportDetailsEveryNamedConstraintKind(t *testing.T) {
	t.Run("unique constraint", func(t *testing.T) {
		described := tbl("t", "id")
		described.UniqueConstraints = []schema.UniqueDef{{Name: "u", Columns: []string{"id"}, OnConflict: schema.ConflictResolution("IGNORE")}}
		live := tbl("t", "id")
		live.UniqueConstraints = []schema.UniqueDef{{Name: "u", Columns: []string{"id"}, OnConflict: schema.ConflictResolution("REPLACE")}}

		change := onlyChange(t, described, live)
		require.Equal(t, catalog.ChangeModified, change.Kind)
		require.Equal(t, `unique constraint "u"`, change.Subject)
		require.Equal(t, []catalog.FieldChange{{Path: "OnConflict", Was: `"IGNORE"`, Now: `"REPLACE"`}}, change.Fields)
	})

	t.Run("check", func(t *testing.T) {
		described := tbl("t", "id")
		described.Checks = []schema.CheckDef{{Name: "c", Expression: "id > 0"}}
		live := tbl("t", "id")
		live.Checks = []schema.CheckDef{{Name: "c", Expression: "id > 0", NotValid: true}}

		change := onlyChange(t, described, live)
		require.Equal(t, catalog.ChangeModified, change.Kind)
		require.Equal(t, `check "c"`, change.Subject)
		require.Equal(t, []catalog.FieldChange{{Path: "NotValid", Was: "false", Now: "true"}}, change.Fields)
	})

	t.Run("index", func(t *testing.T) {
		described := tbl("t", "id")
		described.Indexes = []schema.IndexDef{{Name: "ix", Columns: []string{"id"}, Unique: false}}
		live := tbl("t", "id")
		live.Indexes = []schema.IndexDef{{Name: "ix", Columns: []string{"id"}, Unique: true}}

		change := onlyChange(t, described, live)
		require.Equal(t, catalog.ChangeModified, change.Kind)
		require.Equal(t, `index "ix"`, change.Subject)
		require.Equal(t, []catalog.FieldChange{{Path: "Unique", Was: "false", Now: "true"}}, change.Fields)
	})

	t.Run("foreign key", func(t *testing.T) {
		described := tbl("t", "id")
		described.ForeignKeys = []schema.ForeignKeyDef{{
			Name: "fk", Columns: []string{"id"}, ReferencedTable: "other", ReferencedColumns: []string{"id"},
			OnDelete: schema.ReferenceAction("CASCADE"),
		}}
		live := tbl("t", "id")
		live.ForeignKeys = []schema.ForeignKeyDef{{
			Name: "fk", Columns: []string{"id"}, ReferencedTable: "other", ReferencedColumns: []string{"id"},
			OnDelete: schema.ReferenceAction("SET NULL"),
		}}

		change := onlyChange(t, described, live)
		require.Equal(t, catalog.ChangeModified, change.Kind)
		require.Equal(t, `foreign key "fk"`, change.Subject)
		require.Equal(t, []catalog.FieldChange{{Path: "OnDelete", Was: `"CASCADE"`, Now: `"SET NULL"`}}, change.Fields)
	})

	t.Run("exclusion constraint", func(t *testing.T) {
		described := tbl("t", "id")
		described.ExclusionConstraints = []schema.ExclusionDef{{
			Name: "ex", Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}},
		}}
		live := tbl("t", "id")
		live.ExclusionConstraints = []schema.ExclusionDef{{
			Name: "ex", Method: "gist", Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}},
		}}

		change := onlyChange(t, described, live)
		require.Equal(t, catalog.ChangeModified, change.Kind)
		require.Equal(t, `exclusion constraint "ex"`, change.Subject)
		require.Equal(t, []catalog.FieldChange{{Path: "Method", Was: `""`, Now: `"gist"`}}, change.Fields)
	})
}

// onlyChange drifts described against live, requires exactly one changed
// table carrying exactly one Change, and returns it.
func onlyChange(t *testing.T, described, live schema.TableDef) catalog.Change {
	t.Helper()
	report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
	require.Len(t, report.Changed(), 1)
	changes := report.Changed()[0].Changes()
	require.Len(t, changes, 1)
	return changes[0]
}

// TestReportExplainsEveryChangedTable reuses TestEveryDescriptorFieldIsCompared's
// own field-path walker (catalog_test.go) and additionally requires, for
// every one of its leaf paths, that a table reported as changed also
// reports a non-empty Changes(). This is the §2.1 safety net -- the
// explanation never falls silent about a fact the verdict already noticed
// -- stated as a test rather than as a comment.
func TestReportExplainsEveryChangedTable(t *testing.T) {
	baseline := completenessBaseline()
	leaves := collectCompletenessLeaves(t, baseline)
	require.NotEmpty(t, leaves)

	for _, leaf := range leaves {
		leaf := leaf
		t.Run(leaf.path, func(t *testing.T) {
			described := baseline.Clone()
			live := baseline.Clone()
			leaf.mutate(t, &live)

			report := catalog.Drift([]schema.TableDef{described}, []schema.TableDef{live})
			if leaf.expectNoDrift {
				require.True(t, report.Empty())
				return
			}
			require.False(t, report.Empty())
			require.Len(t, report.Changed(), 1)
			require.NotEmpty(t, report.Changed()[0].Changes(), "path %s changed the verdict but produced no explanation", leaf.path)
		})
	}
}

// TestReportIsDeterministic compares the same inputs twice, and inputs
// whose element lists are supplied in a different order that is not itself
// the difference under test, requiring byte-identical rendered text both
// times.
func TestReportIsDeterministic(t *testing.T) {
	described, live := usersReportFixture()
	legacyUsers := tbl("legacy_users", "id")
	orders := tbl("orders", "id")

	first := catalog.Drift([]schema.TableDef{legacyUsers, described}, []schema.TableDef{live, orders}).String()
	second := catalog.Drift([]schema.TableDef{legacyUsers, described}, []schema.TableDef{live, orders}).String()
	require.Equal(t, first, second)

	// Same tables, different slice order on each side: the report is keyed
	// by QualifiedName, not by input position.
	third := catalog.Drift([]schema.TableDef{described, legacyUsers}, []schema.TableDef{orders, live}).String()
	require.Equal(t, first, third)
}

// TestReportRendersUnexportedOptionalTypes exercises the one panic this
// design has to avoid (§4.4): reflect.Value.Interface() is never called on
// a value reached by drilling into an unexported field, and
// schema.DecimalScale, schema.TextWidth, and schema.IntegerDisplayWidth are
// exactly the three types that keep their state unexported today.
func TestReportRendersUnexportedOptionalTypes(t *testing.T) {
	t.Run("DecimalType.Scale changes", func(t *testing.T) {
		described := tbl("t", "id")
		described.Columns[0].Type = schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}
		live := tbl("t", "id")
		live.Columns[0].Type = schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(4)}

		require.NotPanics(t, func() {
			change := onlyChange(t, described, live)
			require.Equal(t, `column "id"`, change.Subject)
			require.Len(t, change.Fields, 1)
			require.Equal(t, "Type.Scale", change.Fields[0].Path)
			require.Contains(t, change.Fields[0].Was, "value:2")
			require.Contains(t, change.Fields[0].Now, "value:4")
		})
	})

	t.Run("IntegerType gains a DisplayWidth", func(t *testing.T) {
		described := tbl("t", "id")
		described.Columns[0].Type = schema.IntegerType{}
		live := tbl("t", "id")
		live.Columns[0].Type = schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(11), ZeroFill: true}

		require.NotPanics(t, func() {
			change := onlyChange(t, described, live)
			require.Equal(t, `column "id"`, change.Subject)
			require.Equal(t, []catalog.FieldChange{
				{Path: "Type.Unsigned", Was: "false", Now: "true"},
				{Path: "Type.DisplayWidth", Was: "{}", Now: "{value:11 set:true}"},
				{Path: "Type.ZeroFill", Was: "false", Now: "true"},
			}, change.Fields)
		})
	})
}

// TestChangeStringAndKindString pins ChangeKind.String's four names and
// Change.String's separator handling: a Change with an empty Subject
// renders without a stray leading space or colon.
func TestChangeStringAndKindString(t *testing.T) {
	require.Equal(t, "added", catalog.ChangeAdded.String())
	require.Equal(t, "removed", catalog.ChangeRemoved.String())
	require.Equal(t, "modified", catalog.ChangeModified.String())
	require.Equal(t, "moved", catalog.ChangeMoved.String())

	withSubject := catalog.Change{Kind: catalog.ChangeAdded, Subject: `column "x"`}
	require.Equal(t, `+ column "x"`, withSubject.String())

	noSubject := catalog.Change{
		Kind:   catalog.ChangeModified,
		Fields: []catalog.FieldChange{{Path: "Strict", Was: "false", Now: "true"}},
	}
	require.Equal(t, "~ Strict: false -> true", noSubject.String())

	withBoth := catalog.Change{
		Kind:    catalog.ChangeModified,
		Subject: `column "email"`,
		Fields:  []catalog.FieldChange{{Path: "Nullable", Was: "false", Now: "true"}},
	}
	require.Equal(t, `~ column "email": Nullable: false -> true`, withBoth.String())
}

// --- Real-SQLite report content -----------------------------------------
//
// The three misses only SQLite inspection can produce (§1.1): a hidden
// column, a virtual table's own module arguments, and an unnamed
// constraint. Each creates the table twice through modernc.org/sqlite and
// compares the two inspections, rather than asserting rasql's own output
// back to itself.

// TestReportDetectsHiddenColumnAgainstSQLite confirms, against a real
// SQLite engine, that a live fts5 virtual table's own Hidden column fact
// (inspect/inspect.go:786) is exactly what the old comparison never checked
// (§1.1: "ColumnDef.Hidden true to false | nothing at all") and reaches the
// rendered report once compared. Hidden is a per-column property SQLite's
// own grammar has no DDL to flip on an otherwise-identical column in place,
// so the one fact under test is isolated by cloning a real inspection and
// changing only Hidden on one of its columns, rather than by asserting the
// two sides of the comparison both came from the live engine: the shape,
// and the fact that some column in it really does inspect as Hidden, still
// come from modernc.org/sqlite, not from a hand-built fixture.
func TestReportDetectsHiddenColumnAgainstSQLite(t *testing.T) {
	ctx := t.Context()
	database := mustCreateSQLiteDB(t,
		"CREATE VIRTUAL TABLE posts_fts USING fts5(body)",
	)
	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	tableIndex := -1
	for i, table := range tables {
		if table.Name == "posts_fts" {
			tableIndex = i
			break
		}
	}
	require.GreaterOrEqualf(t, tableIndex, 0, "FromDatabase must describe posts_fts itself, not only its fts5 shadow tables; tables: %+v", tables)

	hiddenColumn := -1
	for i, column := range tables[tableIndex].Columns {
		if column.Hidden {
			hiddenColumn = i
			break
		}
	}
	require.GreaterOrEqualf(t, hiddenColumn, 0, "a live fts5 virtual table must report at least one Hidden column (inspect/inspect.go:786); columns: %+v", tables[tableIndex].Columns)

	described := make([]schema.TableDef, len(tables))
	for i, table := range tables {
		described[i] = table.Clone()
	}
	described[tableIndex].Columns[hiddenColumn].Hidden = false
	columnName := described[tableIndex].Columns[hiddenColumn].Name

	report := catalog.Drift(described, tables)
	require.False(t, report.Empty())
	require.Len(t, report.Changed(), 1)

	found := false
	for _, change := range report.Changed()[0].Changes() {
		if change.Subject != fmt.Sprintf("column %q", columnName) {
			continue
		}
		for _, field := range change.Fields {
			if field.Path == "Hidden" {
				require.Equal(t, "false", field.Was)
				require.Equal(t, "true", field.Now)
				found = true
			}
		}
	}
	require.True(t, found, "column %q must report a Hidden field change; report:\n%s", columnName, report.String())
}

func TestReportDetectsVirtualTableModuleArgumentsAgainstSQLite(t *testing.T) {
	ctx := t.Context()
	database := mustCreateSQLiteDB(t,
		"CREATE VIRTUAL TABLE posts_fts USING fts5(body)",
	)
	before, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	_, err = database.ExecContext(ctx, "DROP TABLE posts_fts")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, "CREATE VIRTUAL TABLE posts_fts USING fts5(body, tokenize='porter')")
	require.NoError(t, err)
	after, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.False(t, report.Empty())
	require.Contains(t, report.String(), `~ VirtualTableModuleArguments: ["body"] -> ["body" "tokenize='porter'"]`)
}

func TestReportDetectsUnnamedCheckAndOnConflictAgainstSQLite(t *testing.T) {
	ctx := t.Context()
	database := mustCreateSQLiteDB(t,
		`CREATE TABLE t (
			id INTEGER PRIMARY KEY,
			val INTEGER,
			CHECK (val > 0),
			CHECK (val < 100),
			UNIQUE (val) ON CONFLICT IGNORE
		)`,
	)
	before, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	_, err = database.ExecContext(ctx, "DROP TABLE t")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `CREATE TABLE t (
		id INTEGER PRIMARY KEY,
		val INTEGER,
		CHECK (val >= 0),
		CHECK (val < 100),
		UNIQUE (val) ON CONFLICT REPLACE
	)`)
	require.NoError(t, err)
	after, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	report := catalog.Drift(before, after)
	require.False(t, report.Empty())
	require.Len(t, report.Changed(), 1)

	var addedCheck, removedCheck, addedUnique, removedUnique bool
	for _, change := range report.Changed()[0].Changes() {
		switch {
		case change.Kind == catalog.ChangeAdded && change.Subject == `check {Expression:"val >= 0"}`:
			addedCheck = true
		case change.Kind == catalog.ChangeRemoved && change.Subject == `check {Expression:"val > 0"}`:
			removedCheck = true
		case change.Kind == catalog.ChangeAdded && change.Subject == `unique constraint {Columns:["val"] OnConflict:"REPLACE"}`:
			addedUnique = true
		case change.Kind == catalog.ChangeRemoved && change.Subject == `unique constraint {Columns:["val"] OnConflict:"IGNORE"}`:
			removedUnique = true
		}
	}
	require.True(t, addedCheck, "report:\n%s", report.String())
	require.True(t, removedCheck, "report:\n%s", report.String())
	require.True(t, addedUnique, "report:\n%s", report.String())
	require.True(t, removedUnique, "report:\n%s", report.String())
}
