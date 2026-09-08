package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

// Example_rasqlgen_column_fields contrasts the three ways to name a column,
// and is the one example that reaches for the lower-level two on purpose.
//
// Generate the store, and name columns through the accessor methods it emits.
// That is what every other example here does, and what application code should
// do. rasqlgen writes one accessor method per column from the same descriptor
// the table is created from, so `users.ID().Ref()` is a column reference the
// compiler checks: renaming or dropping a column turns the call sites into
// build failures, instead of leaving queries that assemble happily and fail
// when they run.
//
// The two lower-level forms below exist for the cases a generated accessor
// cannot cover, and each one costs a check the compiler would otherwise have
// made:
//
//   - A plain string names a column when the name is data rather than source
//     code. A table read out of a configuration file or named by an end user
//     has no Go identifier to generate an accessor from, so the name stays a
//     string and rasql checks it against the descriptor as the statement is
//     built.
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
	users := store.Users()
	// A string names the column here because query.NewSelect works without a
	// Go row type, which is exactly the case where no accessor can exist.
	// The cost is visible: the correct name and the typo are the same kind of
	// value, and nothing separates them at this point.
	// BEGIN(string_column)
	correct, err := query.NewSelect(users.Ref(), users.Column("id"))
	if err != nil {
		fmt.Printf("failed to create the correct select: %s\n", err)
		return
	}
	correct, err = correct.WithWhere(query.Equal(users.Column("id"), query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add the correct predicate: %s\n", err)
		return
	}
	typo := users.Column("emial")
	// END(string_column)

	// The correct statement renders successfully, while the invalid runtime
	// column reports its error when validated.
	statement, err := render.Select(dialect.PostgreSQL(), correct)
	if err != nil {
		fmt.Printf("failed to build the correct select: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())
	if err := typo.Validate(); err != nil {
		fmt.Println(err)
	}

	// store.Users() is generated, so its columns are methods. This is the form
	// to write wherever the table is known as it is compiled, because
	// users.Emial() is not a method and the package does not build.
	// BEGIN(typed_column)
	typed, err := query.NewSelect(users.Ref(),
		users.ID().Ref(), users.Email().Ref(), users.Nickname().Ref(),
		users.Status().Ref(), users.FirstName().Ref(), users.LastName().Ref())
	if err == nil {
		typed, err = typed.WithWhere(query.Equal(users.ID().Ref(), query.Bind(42)))
	}
	built, err := render.Select(dialect.PostgreSQL(), typed)
	// END(typed_column)
	if err != nil {
		fmt.Printf("failed to build the typed select: %s\n", err)
		return
	}
	fmt.Println(built.SQL())

	// Column is the escape hatch, shown here with a name a caller would have
	// received as data. It is worth reaching for only when the name is not
	// known as the code is written; a hard-coded "emial" like this one is a
	// bug that users.Email().Ref() would never have compiled. Validate reports the
	// bad name at the lookup, so the caller does not have to assemble a
	// statement to find out.
	// BEGIN(column_lookup)
	column := typo
	// END(column_lookup)
	fmt.Println(column.Name(), column.Validate())

	// Output:
	// SELECT "users"."id" FROM "users" WHERE ("users"."id" = $1)
	// query column: table "users" has no column "emial"
	// SELECT "users"."id", "users"."email", "users"."nickname", "users"."status", "users"."first_name", "users"."last_name" FROM "users" WHERE ("users"."id" = $1)
	// emial query column: table "users" has no column "emial"
}
