package examples_test

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
)

func ExampleStore_objectNames() {
	table := schema.MustTableDef("customer-id", schema.Integer("id"), schema.Text("display-name"))
	store := generate.Store{
		Package: "store",
		Dir:     "generated",
		Tables:  []schema.TableDef{table},
		Names: map[schema.ObjectName]generate.ObjectNames{{Name: "customer-id"}: {
			Accessor: "Customer", TableType: "CustomerTable", RowType: "CustomerRow", FileBase: "customer",
			Columns: map[string]generate.ColumnNames{"display-name": {Field: "DisplayName", Accessor: "DisplayNameColumn"}},
		}},
	}
	plan, err := store.Plan()
	if err != nil {
		fmt.Println(err)
		return
	}
	var source string
	for _, file := range plan.Files() {
		if strings.HasSuffix(file.Path, "customer_gen.go") {
			source = string(file.Source)
		}
	}
	fmt.Println(strings.Contains(source, "CustomerTable"))
	fmt.Println(strings.Contains(source, "DisplayNameColumn"))
	fmt.Println(strings.Contains(source, "\"display-name\""))
	// Output:
	// true
	// true
	// true
}
