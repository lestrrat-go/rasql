package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

// Example_rasql_render_mutation renders a write to SQL text and bound
// arguments without a database. Executing a mutation needs an Executor, which
// carries a connection and a discovered engine profile. Seeing the SQL that
// mutation will send needs neither, so a caller names the profile itself and
// asks a Compiler for the statement, which is what a log line, a review, or a
// test assertion wants.
func Example_rasql_render_mutation() {
	users := store.Users()
	email, err := rasql.BindTypedColumn(users.Email())
	if err != nil {
		fmt.Printf("failed to bind the email column: %s\n", err)
		return
	}
	id, err := rasql.BindTypedColumn(users.ID())
	if err != nil {
		fmt.Printf("failed to bind the id column: %s\n", err)
		return
	}

	plan, err := rasql.NewPatchPlan(users,
		rasql.EqualValue(id.Expr(), int64(1)),
		rasql.SetField(email, "ada@example.com"),
	)
	if err != nil {
		fmt.Printf("failed to build the patch plan: %s\n", err)
		return
	}

	// The profile decides which SQL an engine accepts, so it is named
	// alongside the dialect rather than derived from it.
	profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
	if err != nil {
		fmt.Printf("failed to build the engine profile: %s\n", err)
		return
	}
	compiler, err := profile.Compiler(dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to build the compiler: %s\n", err)
		return
	}

	statement, err := compiler.Mutation(plan)
	if err != nil {
		fmt.Printf("failed to render the mutation: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())
	fmt.Println(statement.Args()...)
	// Output:
	// UPDATE "users" SET "email" = $1 WHERE ("users"."id" = $2)
	// ada@example.com 1
}
