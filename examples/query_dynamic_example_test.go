package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_query_dynamic() {
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
	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add predicate: %s\n", err)
		return
	}
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
