package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

// Example_schema_bind_row_type pairs a table description with the Go type of
// one of its rows, which is the binding rasqlgen performs for a generated
// table. The row type is written out here so the example stands on its own;
// the other examples read the tables generated into examples/store.
func Example_schema_bind_row_type() {
	definition := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)

	// BEGIN(bind_row_type)
	type UserRow struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}

	users := rasql.MustTableOf[UserRow](definition)
	// END(bind_row_type)

	// The bound table is what the typed API takes, so a select from it already
	// knows it returns a UserRow.
	statement, err := rasql.SelectFrom(users).Build(dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users"
}
