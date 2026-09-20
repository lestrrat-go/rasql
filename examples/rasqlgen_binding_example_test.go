package examples_test

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
)

// Example_rasqlgen_binding solves the need to expose domain-specific Go types
// from generated database code. GoBinding tells rasqlgen which type to use for
// a column and which wrapper represents SQL NULL.
func Example_rasqlgen_binding() {
	// BEGIN(binding)
	// The column is nullable, so both the value type and nullable wrapper must
	// be named for generated rows to avoid falling back to string types.
	users := schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{
		Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{
			Type: "UserID", NullableType: "NullableUserID",
		}, Nullable: true,
	}}}
	// Generate the descriptor source in memory because this example needs to
	// inspect the chosen types, not write a package to disk.
	source, err := generate.DescriptorSource("store", []schema.TableDef{users})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Check both the row field and generated wrapper declaration to show that
	// the binding reaches every generated use of the column type.
	fmt.Println(strings.Contains(string(source), "ID NullableUserID"))
	fmt.Println(strings.Contains(string(source), "NullableUserID"))
	// END(binding)
	// Output:
	// true
	// true
}
