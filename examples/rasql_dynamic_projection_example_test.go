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

// orderSummary reads one column from each side of the join, so no generated
// row type fits it: store.UsersRow has no user_id field and store.OrdersRow
// has no email. A local type names exactly the two columns the query selects.
type orderSummary struct {
	UserID int64
	Email  string
}

type orderSummaryDecoder struct{ result rasql.ResultSchema }

func (d orderSummaryDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d orderSummaryDecoder) Presence() []rasql.Presence       { return nil }
func (d orderSummaryDecoder) DecodeRow(src rasql.ScanSource, row *orderSummary) error {
	return src.Scan(&row.UserID, &row.Email)
}

func Example_rasql_dynamic_projection() {
	// This example joins users and orders, then reads an ad hoc result shape.
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
	// Create both descriptors before querying their joined rows.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}

	// Populate both tables through the generated create builders.
	if _, err := rasql.ExecMutation(ctx, executor, store.NewUsersCreate().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Plan()); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 50},
		{ID: 2, UserID: 1, Total: 10},
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

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("user_id", usersID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", usersEmail.Expr(), schema.TextType{}, ""),
	}, orderSummaryDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// SQL: SELECT users.id AS user_id, users.email FROM users INNER JOIN orders ON users.id = orders.user_id WHERE orders.total > ? ORDER BY orders.total DESC (argument: 20)
	q := rasql.Select(usersSource.Source(), projection).
		Join(ordersSource.Source(), rasql.EqualExpr(usersID.Expr(), ordersUserID.Expr())).
		Where(rasql.GreaterValue(ordersTotal.Expr(), int64(20))).
		OrderBy(rasql.DescExpr(ordersTotal.Expr()))
	rows, err := rasql.All(ctx, executor, q)
	if err != nil {
		fmt.Printf("failed to build order totals query: %s\n", err)
		return
	}
	for _, summary := range rows {
		fmt.Println(summary.UserID, summary.Email)
	}

	// Output:
	// 1 ada@example.com
}
