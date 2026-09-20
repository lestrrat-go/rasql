package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Example_query_expressions solves the need to compute values in SQL without
// dropping to a handwritten statement. It composes arithmetic, CAST, and CASE
// expressions while leaving every ordinary value as a bound argument.
func Example_query_expressions() {
	// Reuse bound column expressions so every calculation names the accounts
	// source rather than an unqualified string.
	accounts := query.MustTableRef(schema.MustTableDef("accounts",
		schema.Integer("id"), schema.Integer("balance"), schema.Text("email")))
	id, balance := accounts.Column("id"), accounts.Column("balance")
	// SearchedCase expresses a conditional result without moving the decision
	// into Go after the row has been read.
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
	// Rendering proves that constants become PostgreSQL placeholders rather
	// than being inserted into the SQL text.
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

// Example_query_trustedFragments solves the narrow case where a database
// expression has no query package helper. TrustedSQL keeps identifiers and
// values in distinct typed holes so the renderer can quote and bind them.
func Example_query_trustedFragments() {
	accounts := query.MustTableRef(schema.MustTableDef("accounts", schema.Integer("id"), schema.Integer("balance")))
	// IdentifierHole quotes balance as SQL syntax, while Hole binds 2 as data.
	// Keeping those roles explicit avoids building SQL with string formatting.
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
