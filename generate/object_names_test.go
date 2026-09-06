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

func mustGetwd(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	require.NoError(t, err)
	return directory
}
