package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_rasqlgen_binding() {
	users := schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{
		Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{Type: "UserID"},
	}}}
	if err := generate.Validate("store", users); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("configured")
	// Output: configured
}
