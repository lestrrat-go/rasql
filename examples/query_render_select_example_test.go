package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// BEGIN(render_select)
func Example_query_render_select() {
	// The query and render packages need no database handle and no Go row
	// type. A table description is the only input.
	accounts := query.MustTableRef(schema.MustTableDef("accounts",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	))
	id, err := accounts.Column("id")
	if err != nil {
		fmt.Printf("failed to reference the id column: %s\n", err)
		return
	}
	email, err := accounts.Column("email")
	if err != nil {
		fmt.Printf("failed to reference the email column: %s\n", err)
		return
	}

	// query.NewSelect validates the statement as it builds it.
	statement, err := query.NewSelect(accounts, query.Project(id), query.Project(email))
	if err != nil {
		fmt.Printf("failed to build the select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(email, query.Bind("ada@example.com")))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}

	// One statement renders for whichever dialect it is given. The value
	// stays an argument in both, so it never becomes SQL text.
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.MySQL()} {
		rendered, err := render.Select(d, statement)
		if err != nil {
			fmt.Printf("failed to render the select: %s\n", err)
			return
		}
		fmt.Println(rendered.SQL())
		fmt.Println(rendered.Args()...)
	}

	// Output:
	// SELECT "accounts"."id", "accounts"."email" FROM "accounts" WHERE ("accounts"."email" = $1)
	// ada@example.com
	// SELECT `accounts`.`id`, `accounts`.`email` FROM `accounts` WHERE (`accounts`.`email` = ?)
	// ada@example.com
}

// END(render_select)
