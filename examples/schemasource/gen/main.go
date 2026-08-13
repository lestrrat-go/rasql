// Command gen is the "own the program" alternative to `rasqlgen schema
// -source`: ordinary Go that imports the schema package directly and calls
// generate.WritePackage itself. A user who would rather not invoke
// rasqlgen for this step puts a few lines like these behind their own
// go:generate line instead.
package main

// BEGIN(schema_source_program)
import (
	"fmt"

	"github.com/lestrrat-go/rasql/examples/schemasource"
	"github.com/lestrrat-go/rasql/generate"
)

func main() {
	if err := generate.WritePackage("store", "internal/store", schemasource.Tables()...); err != nil {
		fmt.Printf("failed to write schema package: %s\n", err)
		return
	}
}

// END(schema_source_program)
