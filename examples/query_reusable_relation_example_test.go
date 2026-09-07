package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_queryReusableRelation() {
	users := query.MustTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "active", Type: schema.BooleanType{}},
		},
	})
	filtered, err := query.NewSelect(users, users.Column("id"))
	if err != nil {
		return
	}
	filtered, err = filtered.WithWhere(query.Equal(users.Column("active"), true))
	if err != nil {
		return
	}
	result, err := query.ResultOf(filtered, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		return
	}
	relation, err := query.Derived(result, "active_users")
	if err != nil {
		return
	}
	statement, err := query.NewSelect(relation, relation.Column("id"))
	if err != nil {
		return
	}
	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	if err != nil {
		return
	}
	fmt.Println(rendered.SQL())
	// Output:
	// SELECT "active_users"."id" FROM (SELECT "users"."id" FROM "users" WHERE ("users"."active" = $1)) AS "active_users"
}
