package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Example_queryReusableRelation solves the case where a filtered select must
// become a reusable source for another query. ResultOf declares the subquery's
// output columns, and Derived gives that result an alias the outer select can
// address.
func Example_queryReusableRelation() {
	// Build the inner statement first because its predicate is the behavior the
	// derived relation should preserve.
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
	// ResultOf pairs the statement with the public shape consumers may select
	// from; SQL alone does not carry rasql's column type metadata.
	result, err := query.ResultOf(filtered, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		return
	}
	// Derived assigns the subquery an SQL alias and returns a relation whose
	// columns are qualified by that alias.
	relation, err := query.Derived(result, "active_users")
	if err != nil {
		return
	}
	// The outer query reads the derived column exactly as it would read a table
	// column, while the inner active predicate remains inside the subquery.
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
