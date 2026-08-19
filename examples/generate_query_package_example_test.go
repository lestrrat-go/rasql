package examples_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
)

// Example_generate_query_package builds a generate.QueryPackage from static
// SQL templates alone, plans it, writes it into a scratch directory, and
// checks the result. No database is opened and no table descriptor is
// supplied, which is the whole difference from generate.Store.
//
// One query names a template file and the other carries its template in SQL,
// which is the choice every Query makes.
func Example_generate_query_package() {
	dir, err := os.MkdirTemp("", "rasql-generate-query-package-example-*")
	if err != nil {
		fmt.Printf("failed to create scratch directory: %s\n", err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	template := filepath.Join(dir, "user_by_email.sql")
	if err := os.WriteFile(template, []byte(`SELECT id, email FROM users WHERE email = {{bind "email"}}`), 0o600); err != nil {
		fmt.Printf("failed to write query template: %s\n", err)
		return
	}

	// BEGIN(query_package)
	queries := generate.QueryPackage{
		Package: "queries",
		Dir:     dir,
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: template, Function: "UserByEmail", Output: "user_by_email_gen.go"},
			{SQL: "SELECT count(*) FROM users", Function: "CountUsers", Output: "count_users_gen.go"},
		},
	}

	plan, err := queries.Plan()
	if err != nil {
		fmt.Printf("failed to plan query package: %s\n", err)
		return
	}
	for _, f := range plan.Files() {
		fmt.Println(filepath.Base(f.Path))
	}

	if err := queries.Write(); err != nil {
		fmt.Printf("failed to write query package: %s\n", err)
		return
	}

	if err := queries.Check(); err != nil {
		fmt.Printf("check: %s\n", err)
		return
	}
	fmt.Println("check: ok")
	// END(query_package)

	// Output:
	// count_users_gen.go
	// user_by_email_gen.go
	// check: ok
}
