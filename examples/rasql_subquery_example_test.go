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

// orderSummary projects only orders columns, so no join is needed for the IN
// subquery below: it runs as its own SELECT, never as part of this one.
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
// order. The typed operator set has InQuery for a subquery compared as a set
// of values, but no way to embed a scalar subquery as a comparison operand,
// so the average is computed as its own round trip and then compared as a
// bound value rather than inline in one statement.
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

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Printf("failed to describe engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
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
		plan := store.NewUsersCreate().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 80},
		{ID: 2, UserID: 2, Total: 20},
		{ID: 3, UserID: 3, Total: 100},
	} {
		plan := store.NewOrdersCreate().ID(order.ID).UserID(order.UserID).Total(order.Total).Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	usersSource, err := rasql.SourceOf(users, "")
	if err != nil {
		fmt.Printf("failed to bind users source: %s\n", err)
		return
	}
	usersID, err := rasql.BindColumn[store.UsersRow, int64](usersSource, users.IDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind users id column: %s\n", err)
		return
	}
	usersEmail, err := rasql.BindColumn[store.UsersRow, string](usersSource, users.EmailRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind users email column: %s\n", err)
		return
	}
	ordersSource, err := rasql.SourceOf(orders, "")
	if err != nil {
		fmt.Printf("failed to bind orders source: %s\n", err)
		return
	}
	ordersUserID, err := rasql.BindColumn[store.OrdersRow, int64](ordersSource, orders.UserIDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind orders user_id column: %s\n", err)
		return
	}
	ordersTotal, err := rasql.BindColumn[store.OrdersRow, int64](ordersSource, orders.TotalRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind orders total column: %s\n", err)
		return
	}

	// The average is computed first, as its own statement.
	// SQL: SELECT AVG(orders.total) AS average FROM orders
	averageProjection, err := rasql.NullableScalar("average", rasql.AvgExpr(ordersTotal.Expr()), schema.FloatType{}, "")
	if err != nil {
		fmt.Printf("failed to build average projection: %s\n", err)
		return
	}
	average, err := rasql.One(ctx, executor, rasql.Select(ordersSource.Source(), averageProjection))
	if err != nil {
		fmt.Printf("failed to compute average order total: %s\n", err)
		return
	}

	// domainUsers selects the id of every user whose email ends in the chosen
	// domain. It reads no table of the enclosing statement, so it validates and
	// runs as its own SELECT.
	domainUsersProjection, err := rasql.Scalar("id", usersID.Expr(), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to build domain-users projection: %s\n", err)
		return
	}
	domainUsers := rasql.Select(usersSource.Source(), domainUsersProjection).
		Where(rasql.LikeValue(usersEmail.Expr(), "%@example.com"))
	inDomain, err := rasql.InQuery(ordersUserID.Expr(), domainUsers)
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
		rasql.Item("user_id", ordersUserID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("total", ordersTotal.Expr(), schema.IntegerType{}, ""),
	}, subqueryOrderSummaryDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// SQL: SELECT orders.user_id, orders.total FROM orders WHERE orders.user_id IN (SELECT users.id FROM users WHERE users.email LIKE ?) AND orders.total >= ? ORDER BY orders.total ASC (arguments: "%@example.com", 66.67)
	rows, err := rasql.All(ctx, executor,
		rasql.Select(ordersSource.Source(), projection).
			Where(rasql.And(inDomain, rasql.GreaterOrEqualValue(ordersTotal.Expr(), int64(average.Value)))).
			OrderBy(rasql.AscExpr(ordersTotal.Expr())))
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for _, summary := range rows {
		fmt.Println(summary.UserID, summary.Total)
	}

	// Output:
	// 1 80
}
