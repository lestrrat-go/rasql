package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_query_dynamic() {
	// schema.Table declares the columns available to the query builder. A table
	// reference validates the descriptor once and keeps the query isolated from
	// other tables that may have the same column names.
	users, err := query.NewTableRef(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	if err != nil {
		fmt.Printf("failed to create table reference: %s\n", err)
		return
	}
	// Column references are tied to this table reference. They can be used for
	// projections, predicates, joins, and ordering without manually qualifying
	// their SQL identifiers.
	id, err := users.Column("id")
	if err != nil {
		fmt.Printf("failed to select id column: %s\n", err)
		return
	}
	email, err := users.Column("email")
	if err != nil {
		fmt.Printf("failed to select email column: %s\n", err)
		return
	}
	// NewSelect returns an immutable statement. Each With method returns a new
	// validated statement, so the original can be safely reused by another path.
	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	// Bind keeps the value separate from SQL text. The renderer assigns the
	// placeholder syntax required by the selected database dialect.
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add predicate: %s\n", err)
		return
	}
	// The rendered statement contains SQL and arguments ready for runtime.Client
	// or database/sql. The value 42 is never interpolated into SQL text.
	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	if err != nil {
		fmt.Printf("failed to render select: %s\n", err)
		return
	}

	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args())

	// Output:
	// SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" = $1)
	// [42]
}
