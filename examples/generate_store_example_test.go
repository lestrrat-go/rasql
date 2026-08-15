package examples_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
)

// Example_generate_store builds a generate.Store over a hand-written table
// definition the way a user's own gen/main.go would, plans it, writes it into
// a scratch directory, and checks the result -- all through generate's
// exported surface, from a package outside generate itself.
func Example_generate_store() {
	dir, err := os.MkdirTemp("", "rasql-generate-store-example-*")
	if err != nil {
		fmt.Printf("failed to create scratch directory: %s\n", err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	widgets := schema.MustTableDef("widgets",
		schema.Integer("id"),
		schema.Text("name"),
		schema.PrimaryKey("id"),
	)

	store := generate.Store{
		Package: "store",
		Dir:     dir,
		Tables:  []schema.TableDef{widgets},
	}

	plan, err := store.Plan()
	if err != nil {
		fmt.Printf("failed to plan store: %s\n", err)
		return
	}
	for _, f := range plan.Files() {
		fmt.Println(filepath.Base(f.Path))
	}

	if err := store.Write(); err != nil {
		fmt.Printf("failed to write store: %s\n", err)
		return
	}

	if err := store.Check(); err != nil {
		fmt.Printf("check: %s\n", err)
		return
	}
	fmt.Println("check: ok")

	// Output:
	// schema_gen.go
	// schema_gen_test.go
	// widgets_gen.go
	// check: ok
}
