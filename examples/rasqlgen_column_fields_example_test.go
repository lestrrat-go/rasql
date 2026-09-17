package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
)

// Example_rasqlgen_column_fields contrasts the two ways to name a column, and
// is the one example that reaches for the weaker one on purpose.
//
// Generate the store, bind its columns with the generated columns struct, and
// name each one as a field of the result. That is what every other example
// here does, and what application code should do. rasqlgen derives one field
// per column from the same descriptor the table is created from, so
// `columns.ID` is a column reference the compiler checks: renaming or dropping
// a column turns the field references into build failures, instead of leaving
// queries that assemble happily and fail when they run.
//
// `Table.Column(name)` names a column by string, and costs a check the
// compiler would otherwise have made. Two cases need it:
//
//   - The name arrives as data rather than as source code. A table read out of
//     a configuration file or named by an end user has no Go identifier to
//     generate a field from, so the name stays a string and rasql checks it
//     against the descriptor as the statement is built, or on demand through
//     `ColumnRef.Validate`.
//   - The statement is built with the `query` package, which takes a
//     `query.ColumnRef` and knows nothing about Go row types. The generated
//     columns struct binds a `rasql.Column` instead, so `Column` is the only
//     way across.
//
// Reaching for the string where the generated field would do gives up the
// compile-time check and gains nothing, which is why the rest of the
// documentation does not do it.
func Example_rasqlgen_column_fields() {
	users := store.Users()
	// A string names the column here because query.NewSelect works without a
	// Go row type, which is exactly the case where no bound column can exist.
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

	// store.UsersColumns is generated, so its columns are fields. This is the
	// form to write wherever the table is known as it is compiled, because
	// columns.Emial is not a field and the package does not build.
	// BEGIN(typed_column)
	columns, err := (store.UsersColumns{}).Bind(users.Table)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	projection, err := store.UsersProjection(columns)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	typed := rasql.Select(users, projection).
		Where(rasql.EqualValue(columns.ID.Expr(), int64(42)))
	built, err := rasql.Render(typed, dialect.PostgreSQL())
	// END(typed_column)
	if err != nil {
		fmt.Printf("failed to build the typed select: %s\n", err)
		return
	}
	fmt.Println(built.SQL())

	// Column is the escape hatch, shown here with a name a caller would have
	// received as data. It is worth reaching for only when the name is not
	// known as the code is written; a hard-coded "emial" like this one is a
	// bug that columns.Email would never have compiled. Validate reports the
	// bad name at the lookup, so the caller does not have to assemble a
	// statement to find out.
	// BEGIN(column_lookup)
	column := typo
	// END(column_lookup)
	fmt.Println(column.Name(), column.Validate())

	// Output:
	// SELECT "users"."id" FROM "users" WHERE ("users"."id" = $1)
	// query column: table "users" has no column "emial"
	// SELECT "users"."id" AS "id", "users"."email" AS "email", "users"."nickname" AS "nickname", "users"."status" AS "status", "users"."first_name" AS "first_name", "users"."last_name" AS "last_name" FROM "users" WHERE ("users"."id" = $1)
	// emial query column: table "users" has no column "emial"
}
