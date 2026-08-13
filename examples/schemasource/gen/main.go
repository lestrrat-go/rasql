// Command gen is the "own the program" alternative to `rasqlgen schema
// -source`: ordinary Go that imports the schema package directly and calls
// generate.WritePackage itself. A user who would rather not invoke
// rasqlgen for this step puts a few lines like these behind their own
// generate directive instead.
//
// The generate directive lives in the schema package next door, so this
// program runs with its working directory at examples/schemasource and
// writes into the internal/store directory there.
package main

// BEGIN(schema_source_program)
import (
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/examples/schemasource"
	"github.com/lestrrat-go/rasql/generate"
)

func main() {
	if err := generate.WritePackage("store", "internal/store", schemasource.Tables()...); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write schema package: %s\n", err)
		os.Exit(1)
	}
}

// END(schema_source_program)
