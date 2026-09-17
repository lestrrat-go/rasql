package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// orderSummary projects only orders columns, so no join is needed for either
// subquery below: each runs as its own SELECT, never as part of this one.
// store.OrdersRow would decode these two columns as well, but it maps the
// whole table, so its id field would read 0 whether or not the database sent
// one. A type holding just the projected columns says what was asked for.
type subqueryOrderSummary struct {
	UserID int64
	Total  int64
}

type subqueryOrderSummaryDecoder struct{ result rasql.ResultSchema }

func (d subqueryOrderSummaryDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d subqueryOrderSummaryDecoder) Presence() []rasql.Presence       { return nil }
func (d subqueryOrderSummaryDecoder) DecodeRow(src rasql.ScanSource, row *subqueryOrderSummary) error {
	return src.Scan(&row.UserID, &row.Total)
}

// Example_rasql_subquery selects orders placed by a user reachable by email
// domain, then narrows to orders at or above the average total across every
// order, both as scalar subqueries nested inside one statement: InQuery
// covers the domain membership test, and SubqueryExpr lifts the average into
// a plain comparison operand.
func Example_rasql_subquery() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	orders := store.Orders()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@other.example"},
	} {
		plan, err := store.NewUsersCreate().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Plan()
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		if _, err := rasql.ExecMutation(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 80},
		{ID: 2, UserID: 2, Total: 20},
		{ID: 3, UserID: 3, Total: 100},
	} {
		plan, err := store.NewOrdersCreate().ID(order.ID).UserID(order.UserID).Total(order.Total).Plan()
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		if _, err := rasql.ExecMutation(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	usersColumns, err := (store.UsersColumns{}).Bind(users.Table)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	ordersColumns, err := (store.OrdersColumns{}).Bind(orders.Table)
	if err != nil {
		fmt.Printf("failed to bind orders columns: %s\n", err)
		return
	}
	// The average subquery below compares against orders.total as a float64,
	// since AvgExpr always returns NullExpr[float64] regardless of its input
	// column's type and GreaterOrEqualExpr requires both sides to share the
	// same Go type. The generated ordersColumns.Total mirrors the row's stored
	// int64 instead, so this second, string-named bind of the same "total"
	// column is what gives the comparison a matching float64 view; the
	// generated columns struct carries one Go type per column and no more.
	ordersTotalAsFloat, err := rasql.BindColumn[store.OrdersRow, float64](orders, "total", "")
	if err != nil {
		fmt.Printf("failed to bind orders total column as float64: %s\n", err)
		return
	}

	// allOrders aliases orders so the average subquery is a separate scope
	// from the orders read by the enclosing statement, even though it names
	// the same table, following the same pattern a correlated subquery would
	// use to distinguish an inner row from an outer one.
	allOrders, err := orders.As("all_orders")
	if err != nil {
		fmt.Printf("failed to alias orders: %s\n", err)
		return
	}
	allOrdersColumns, err := (store.OrdersColumns{}).Bind(allOrders)
	if err != nil {
		fmt.Printf("failed to bind all_orders columns: %s\n", err)
		return
	}
	// AVG is NULL over an empty group; COALESCE turns it into a plain,
	// never-NULL Expr, which is what averageProjection needs. The orders
	// table is never empty here, so this fallback is never actually read.
	averageValue := rasql.CoalesceExpr(rasql.AvgExpr(allOrdersColumns.Total.Expr()), rasql.Value(0.0))
	averageProjection, err := rasql.Scalar("average", averageValue, schema.FloatType{}, "")
	if err != nil {
		fmt.Printf("failed to build average projection: %s\n", err)
		return
	}
	averageQuery := rasql.Select(allOrders, averageProjection)
	averageSubquery, err := rasql.SubqueryExpr(averageQuery)
	if err != nil {
		fmt.Printf("failed to build the average subquery expression: %s\n", err)
		return
	}
	// SubqueryExpr's own return is NullExpr regardless, since it can never
	// promise averageQuery returns a row; a second COALESCE turns that into
	// the plain Expr GreaterOrEqualExpr requires. This fallback is also
	// never actually read: an aggregate query without GROUP BY always
	// returns exactly one row.
	averageExpr := rasql.CoalesceExpr(averageSubquery, rasql.Value(0.0))

	// domainUsers selects the id of every user whose email ends in the chosen
	// domain. It reads no table of the enclosing statement, so it validates and
	// runs as its own SELECT.
	domainUsersProjection, err := rasql.Scalar("id", usersColumns.ID.Expr(), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to build domain-users projection: %s\n", err)
		return
	}
	domainUsers := rasql.Select(users, domainUsersProjection).
		Where(rasql.LikeValue(usersColumns.Email.Expr(), "%@example.com"))
	inDomain, err := rasql.InQuery(ordersColumns.UserID.Expr(), domainUsers)
	if err != nil {
		fmt.Printf("failed to build the in-domain predicate: %s\n", err)
		return
	}

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("user_id", ordersColumns.UserID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("total", ordersColumns.Total.Expr(), schema.IntegerType{}, ""),
	}, subqueryOrderSummaryDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// InQuery keeps orders placed by a domain user without costing one
	// argument per candidate id, and SubqueryExpr compares the total against
	// the average of every order, both nested inside the one statement that
	// runs below.
	selected := rasql.Select(orders, projection).
		Where(rasql.And(inDomain, rasql.GreaterOrEqualExpr(ordersTotalAsFloat.Expr(), averageExpr))).
		OrderBy(rasql.AscExpr(ordersColumns.Total.Expr()))

	statement, err := rasql.Render(selected, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	rows, err := rasql.All(ctx, db, selected)
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for _, summary := range rows {
		fmt.Println(summary.UserID, summary.Total)
	}

	// Output:
	// SELECT "orders"."user_id" AS "user_id", "orders"."total" AS "total" FROM "orders" WHERE (("orders"."user_id" IN (SELECT "users"."id" AS "id" FROM "users" WHERE ("users"."email" LIKE ?))) AND ("orders"."total" >= COALESCE((SELECT COALESCE(AVG("all_orders"."total"), ?) AS "average" FROM "orders" AS "all_orders"), ?))) ORDER BY "orders"."total"
	// 1 80
}
