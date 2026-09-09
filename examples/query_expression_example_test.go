package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

func Example_query_expressions() {
	accounts := query.MustTableRef(schema.MustTableDef("accounts",
		schema.Integer("id"), schema.Integer("balance"), schema.Text("email")))
	id, balance := accounts.Column("id"), accounts.Column("balance")
	label := query.SearchedCase(
		query.When(query.GreaterThan(balance, 100), "large"),
	).Else("small")
	statement, err := query.NewSelect(accounts, query.Project(query.CastAs(query.Add(balance, 1), schema.IntegerType{})).As("next"), query.Project(label).As("size"))
	if err != nil {
		fmt.Println(err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(id, 1))
	if err != nil {
		fmt.Println(err)
		return
	}
	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)
	// Output:
	// SELECT CAST(("accounts"."balance" + $1) AS BIGINT) AS "next", (CASE WHEN ("accounts"."balance" > $2) THEN $3 ELSE $4 END) AS "size" FROM "accounts" WHERE ("accounts"."id" = $5)
	// 1 100 large small 1
}

func Example_query_trustedFragments() {
	accounts := query.MustTableRef(schema.MustTableDef("accounts", schema.Integer("id"), schema.Integer("balance")))
	fragment := query.TrustedSQL("{} + {}", query.IdentifierHole(query.Ident("balance")), query.Hole(2))
	statement, err := query.NewSelect(accounts, query.Project(fragment).As("total"))
	if err != nil {
		fmt.Println(err)
		return
	}
	rendered, err := render.Select(dialect.SQLite(), statement)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)
	// Output:
	// SELECT "balance" + ? AS "total" FROM "accounts"
	// 2
}
