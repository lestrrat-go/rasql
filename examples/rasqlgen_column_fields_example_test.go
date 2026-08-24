package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/examples/store"
)

// Example_rasqlgen_column_fields contrasts the three ways to name a column,
// and is the one example that reaches for the lower-level two on purpose.
//
// Generate the store, and name columns through the accessor methods it emits.
// That is what every other example here does, and what application code should
// do. rasqlgen writes one accessor method per column from the same descriptor
// the table is created from, so `users.ID()` is a column reference the
// compiler checks: renaming or dropping a column turns the call sites into
// build failures, instead of leaving queries that assemble happily and fail
// when they run.
//
// The two lower-level forms below exist for the cases a generated accessor
// cannot cover, and each one costs a check the compiler would otherwise have
// made:
//
//   - A plain string names a column when the name is data rather than source
//     code, which is what `rasql/dynamic` is for. A table read out of a
//     configuration file or named by an end user has no Go identifier to
//     generate an accessor from, so the name stays a string and rasql checks
//     it against the descriptor as the statement is built.
//   - `Table.Column(name)` names a column on a typed table when the name is
//     only known while the program runs. It is the escape hatch for a caller
//     that has generated code in hand but a name that arrives as data, and
//     `ColumnRef.Validate` reports a bad name at the lookup rather than
//     leaving it for the statement that carries it.
//
// Neither form is a shorter spelling of the generated accessor. Reaching for
// one where an accessor exists gives up the compile-time check and gains
// nothing, which is why the rest of the documentation does not do it.
func Example_rasqlgen_column_fields() {
	// A string names the column here because dynamic.SelectFrom works without
	// a Go row type, which is exactly the case where no accessor can exist.
	// The cost is visible: the correct name and the typo are the same kind of
	// value, and nothing separates them at this point.
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

	// store.Users() is generated, so its columns are methods. This is the form
	// to write wherever the table is known as it is compiled, because
	// users.Emial() is not a method and the package does not build.
	// BEGIN(typed_column)
	users := store.Users()
	built, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 42).Build(dialect.PostgreSQL())
	// END(typed_column)
	if err != nil {
		fmt.Printf("failed to build the typed select: %s\n", err)
		return
	}
	fmt.Println(built.SQL())

	// Column is the escape hatch, shown here with a name a caller would have
	// received as data. It is worth reaching for only when the name is not
	// known as the code is written; a hard-coded "emial" like this one is a
	// bug that users.Email() would never have compiled. Validate reports the
	// bad name at the lookup, so the caller does not have to assemble a
	// statement to find out.
	// BEGIN(column_lookup)
	column := users.Column("emial")
	// END(column_lookup)
	fmt.Println(column.Name(), column.Validate())

	// Output:
	// SELECT "users"."id" FROM "users" WHERE ("users"."id" = $1)
	// query column: table "users" has no column "emial"
	// SELECT "users"."id", "users"."email", "users"."nickname", "users"."status", "users"."first_name", "users"."last_name" FROM "users" WHERE ("users"."id" = $1)
	// emial query column: table "users" has no column "emial"
}
