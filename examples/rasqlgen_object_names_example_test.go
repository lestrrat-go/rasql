package examples_test

import (
	"fmt"
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
	_, err := store.Plan()
	fmt.Println(err == nil)
	// Output: true
}
