package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/examples/store"
)

// Example_rasqlgen_column_fields contrasts the two ways to name a column. A
// string is checked when the statement is rendered, so a typo survives until
// then; a generated column accessor is checked by the compiler, so the same
// typo never builds.
func Example_rasqlgen_column_fields() {
	// BEGIN(string_column)
	correct := dynamic.SelectFrom(store.Users().Ref()).Select("id").WhereEqual("id", 42)
	typo := dynamic.SelectFrom(store.Users().Ref()).Select("id").WhereEqual("emial", 42)
	// END(string_column)

	// Nothing separates the two until one of them is rendered.
	statement, err := correct.Build(dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to build the correct select: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())
	if _, err := typo.Build(dialect.PostgreSQL()); err != nil {
		fmt.Println(err)
	}

	// BEGIN(typed_column)
	users := store.Users()
	built, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 42).Build(dialect.PostgreSQL())
	// END(typed_column)
	if err != nil {
		fmt.Printf("failed to build the typed select: %s\n", err)
		return
	}
	fmt.Println(built.SQL())

	// A name looked up while the program runs is checked by calling Validate,
	// which reports a bad one at the call that supplied it.
	// BEGIN(column_lookup)
	column := users.Column("emial")
	// END(column_lookup)
	fmt.Println(column.Name(), column.Validate())

	// Output:
	// SELECT "users"."id" FROM "users" WHERE ("users"."id" = $1)
	// query column: table "users" has no column "emial"
	// SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)
	// emial query column: table "users" has no column "emial"
}
