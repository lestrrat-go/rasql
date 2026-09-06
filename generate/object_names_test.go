package generate_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestStoreObjectNamesPreservePhysicalNames(t *testing.T) {
	descriptor := schema.MustTableDef("customer-id",
		schema.Integer("id"), schema.Text("scan_row"), schema.Text("foo_bar"), schema.Text("foo__bar"),
	)
	dir := t.TempDir()
	store := generate.Store{
		Package: "generated",
		Dir:     dir,
		Root:    filepath.Dir(mustGetwd(t)),
		Tables:  []schema.TableDef{descriptor},
		Names: map[schema.ObjectName]generate.ObjectNames{{Name: "customer-id"}: {
			Accessor:  "Customer",
			TableType: "CustomerTable",
			RowType:   "CustomerRow",
			FileBase:  "customer",
			Columns: map[string]generate.ColumnNames{
				"scan_row": {Field: "ScanRowValue", Accessor: "ScanRowColumn"},
				"foo_bar":  {Field: "FooBarValue", Accessor: "FooBarColumn"},
				"foo__bar": {Field: "FooBarOther", Accessor: "FooBarOtherColumn"},
			},
		}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	for _, file := range plan.Files() {
		_, parseErr := parser.ParseFile(token.NewFileSet(), file.Path, file.Source, 0)
		require.NoError(t, parseErr)
		if filepath.Base(file.Path) == "customer_gen.go" {
			require.Contains(t, string(file.Source), `"customer-id"`)
			require.Contains(t, string(file.Source), "ScanRowValue")
			require.Contains(t, string(file.Source), "ScanRowColumn")
		}
	}
}

func TestStoreObjectNamesRejectInvalidConfiguration(t *testing.T) {
	table := schema.MustTableDef("users", schema.Integer("id"), schema.Text("name"))
	base := func(names map[schema.ObjectName]generate.ObjectNames) generate.Store {
		return generate.Store{Package: "generated", Dir: t.TempDir(), Root: filepath.Dir(mustGetwd(t)), Tables: []schema.TableDef{table}, Names: names}
	}
	_, err := base(map[schema.ObjectName]generate.ObjectNames{{Name: "missing"}: {Accessor: "Missing"}}).Plan()
	require.ErrorContains(t, err, "unknown table")
	_, err = base(map[schema.ObjectName]generate.ObjectNames{{Name: "users"}: {Columns: map[string]generate.ColumnNames{"missing": {Field: "Missing"}}}}).Plan()
	require.ErrorContains(t, err, "unknown column")
	_, err = base(map[schema.ObjectName]generate.ObjectNames{{Name: "users"}: {Columns: map[string]generate.ColumnNames{
		"id": {Field: "Same"}, "name": {Field: "Same"},
	}}}).Plan()
	require.ErrorContains(t, err, "duplicate final field")
	_, err = base(map[schema.ObjectName]generate.ObjectNames{{Name: "users"}: {FileBase: "Bad-Name"}}).Plan()
	require.ErrorContains(t, err, "invalid FileBase")
	legacy := table
	legacy.RowName = "LegacyRow"
	store := base(nil)
	store.Tables = []schema.TableDef{legacy}
	store.Names = map[schema.ObjectName]generate.ObjectNames{{Name: "users"}: {RowType: "OtherRow"}}
	_, err = store.Plan()
	require.ErrorContains(t, err, "conflicts with legacy RowName")
}

func TestStoreObjectNamesPlanIsHeldSnapshot(t *testing.T) {
	table := schema.MustTableDef("customer_id", schema.Integer("id"), schema.Text("name"))
	table.PrimaryKey = []string{"id"}
	table.UniqueConstraints = []schema.UniqueDef{{Columns: []string{"name"}}}
	table.Indexes = []schema.IndexDef{{Name: "customer_name_idx", Columns: []string{"name"}}}
	table.ForeignKeys = []schema.ForeignKeyDef{{Columns: []string{"id"}, ReferencedTable: "customer-id", ReferencedColumns: []string{"id"}, OnDelete: schema.Cascade}}
	table.Relationships = []schema.RelationshipDef{{Name: "Self", Kind: schema.RelationshipBelongsTo, Columns: []string{"id"}, ReferencedTable: "customer-id", ReferencedColumns: []string{"id"}}}
	table.Columns[1].GeneratedExpression = "CustomerName"
	table.Columns[1].GeneratedStorage = schema.GeneratedStored
	store := generate.Store{
		Package: "generated", Dir: t.TempDir(), Root: filepath.Dir(mustGetwd(t)),
		Tables: []schema.TableDef{table},
		Names: map[schema.ObjectName]generate.ObjectNames{{Name: "customer_id"}: {
			Accessor: "Customer", Columns: map[string]generate.ColumnNames{"name": {Field: "CustomerName"}},
		}},
		Hints: map[string]generate.TableHint{"customer_id": {RowName: "CustomerRow"}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	want := plan.Files()
	store.Names[schema.ObjectName{Name: "customer_id"}] = generate.ObjectNames{Accessor: "Changed"}
	changed := store.Names[schema.ObjectName{Name: "customer_id"}]
	changed.Columns = map[string]generate.ColumnNames{"name": {Field: "ChangedName"}}
	store.Names[schema.ObjectName{Name: "customer_id"}] = changed
	store.Tables[0].Name, store.Tables[0].Schema = "changed", "other"
	store.Tables[0].Columns[0].Name = "changed-id"
	store.Tables[0].Columns = append(store.Tables[0].Columns, schema.ColumnDef{Name: "extra", Type: schema.TextType{}})
	store.Tables[0].PrimaryKey[0] = "changed-id"
	store.Tables[0].UniqueConstraints[0].Columns[0] = "changed-name"
	store.Tables[0].Indexes[0].Columns[0] = "changed-name"
	store.Tables[0].ForeignKeys[0].ReferencedTable = "changed"
	store.Tables[0].ForeignKeys[0].Columns[0] = "changed-id"
	store.Tables[0].Relationships[0].ReferencedTable = "changed"
	store.Tables[0].Relationships[0].Columns[0] = "changed-id"
	store.Tables[0].Columns[1].Default = "changed-default"
	store.Tables[0].Columns[1].GeneratedExpression = "changed-generated"
	store.Hints["customer_id"] = generate.TableHint{RowName: "ChangedRow"}
	got := plan.Files()
	require.Equal(t, want, got)
	got[0].Source[0] ^= 0xff
	require.Equal(t, want, plan.Files())
}

func TestStoreObjectNamesKeepExistingSurfaceStable(t *testing.T) {
	table := schema.MustTableDef("customer-id", schema.Integer("id"), schema.Text("name"))
	store := generate.Store{Package: "generated", Dir: t.TempDir(), Root: filepath.Dir(mustGetwd(t)), Tables: []schema.TableDef{table}, Names: map[schema.ObjectName]generate.ObjectNames{{Name: "customer-id"}: {Accessor: "Customer", TableType: "CustomerTable", RowType: "CustomerRow", FileBase: "customer", Columns: map[string]generate.ColumnNames{"name": {Field: "DisplayName", Accessor: "DisplayNameColumn"}}}}}
	first, err := store.Plan()
	require.NoError(t, err)
	before := first.Files()
	store.Tables[0].Columns = append(store.Tables[0].Columns, schema.ColumnDef{Name: "unrelated", Type: schema.TextType{}})
	second, err := store.Plan()
	require.NoError(t, err)
	after := second.Files()
	require.Equal(t, len(before), len(after))
	for i := range before {
		require.Equal(t, before[i].Path, after[i].Path)
		if filepath.Base(after[i].Path) == "customer_gen.go" {
			require.Contains(t, string(after[i].Source), "CustomerTable")
			require.Contains(t, string(after[i].Source), "DisplayNameColumn")
		}
	}
}

func TestStoreObjectNamesPreserveDescriptorOwners(t *testing.T) {
	parent := schema.MustTableDef("parent", schema.Integer("parent-id"), schema.PrimaryKey("parent-id"))
	child := schema.MustTableDef("child", schema.Integer("child-id"), schema.Integer("parent-id"), schema.Text("same-Go-name"), schema.PrimaryKey("child-id"), schema.Unique("same-Go-name"), schema.Check("same-Go-name <> ''"), schema.Index("same_idx", "same-Go-name"))
	child.ForeignKeys = []schema.ForeignKeyDef{{Columns: []string{"parent-id"}, ReferencedTable: "parent", ReferencedColumns: []string{"parent-id"}, OnDelete: schema.SetNull}}
	child.Relationships = []schema.RelationshipDef{{Name: "Parent", Kind: schema.RelationshipBelongsTo, Columns: []string{"parent-id"}, ReferencedTable: "parent", ReferencedColumns: []string{"parent-id"}}}
	store := generate.Store{Package: "generated", Dir: t.TempDir(), Root: filepath.Dir(mustGetwd(t)), Tables: []schema.TableDef{parent, child}, Names: map[schema.ObjectName]generate.ObjectNames{{Name: "parent"}: {Accessor: "Parent", Columns: map[string]generate.ColumnNames{"parent-id": {Field: "ParentID", Accessor: "ParentIDColumn"}}}, {Name: "child"}: {Accessor: "Child", Columns: map[string]generate.ColumnNames{"child-id": {Field: "ChildID", Accessor: "ChildIDColumn"}, "parent-id": {Field: "ParentIDValue", Accessor: "ParentIDColumn"}, "same-Go-name": {Field: "SameGoName", Accessor: "SameGoNameColumn"}}}}}
	plan, err := store.Plan()
	require.NoError(t, err)
	for _, file := range plan.Files() {
		if filepath.Base(file.Path) == "schema_gen.go" {
			source := string(file.Source)
			for _, physical := range []string{"parent-id", "child-id", "same-Go-name", "same-Go-name <> ''"} {
				require.Contains(t, source, physical)
			}
		}
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	require.NoError(t, err)
	return directory
}
