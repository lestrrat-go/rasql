package examples_test

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_rasqlgen_binding() {
	// BEGIN(binding)
	users := schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{
		Name: "id", Type: schema.TextType{}, GoBinding: &schema.GoBinding{
			Type: "UserID", NullableType: "NullableUserID",
		}, Nullable: true,
	}}}
	source, err := generate.PackageSource("store", users)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(strings.Contains(string(source), "ID NullableUserID"))
	fmt.Println(strings.Contains(string(source), "NullableUserID"))
	// END(binding)
	// Output:
	// true
	// true
}
