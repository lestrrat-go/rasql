package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// BEGIN(correlated_projection)

func Example_query_correlated_projection() {
	users := query.MustTableRef(schema.MustTableDef("users", schema.Integer("id")))
	orders := query.MustTableRef(schema.MustTableDef(
		"orders",
		schema.Integer("id"), schema.Integer("user_id"), schema.Integer("amount"),
	))

	// The constructor declares users before it validates the projection, so the
	// projection can read both the order and the enclosing user's columns.
	ordersForUser, err := query.NewCorrelatedSelect(
		orders, []query.RelationSource{users},
		query.Project(query.Coalesce(orders.Column("amount"), users.Column("id"))).As("value"),
	)
	if err != nil {
		fmt.Printf("failed to build correlated select: %s\n", err)
		return
	}
	ordersForUser, err = ordersForUser.WithWhere(query.Equal(orders.Column("user_id"), users.Column("id")))
	if err != nil {
		fmt.Printf("failed to add correlation predicate: %s\n", err)
		return
	}
	statement, err := query.NewSelect(users, users.Column("id"), query.Project(query.Scalar(ordersForUser)).As("value"))
	if err != nil {
		fmt.Printf("failed to build outer select: %s\n", err)
		return
	}
	rendered, err := render.Select(dialect.SQLite(), statement)
	if err != nil {
		fmt.Printf("failed to render select: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())

	// Output:
	// SELECT "users"."id", (SELECT COALESCE("orders"."amount", "users"."id") AS "value" FROM "orders" WHERE ("orders"."user_id" = "users"."id")) AS "value" FROM "users"
}

// END(correlated_projection)
