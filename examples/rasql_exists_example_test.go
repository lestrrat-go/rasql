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

// userSummary projects the two user columns this example prints.
type userSummary struct {
	ID    int64
	Email string
}

type userSummaryDecoder struct{ result rasql.ResultSchema }

func (d userSummaryDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userSummaryDecoder) Presence() []rasql.Presence       { return nil }
func (d userSummaryDecoder) DecodeRow(src rasql.ScanSource, row *userSummary) error {
	return src.Scan(&row.ID, &row.Email)
}

// Example_rasql_exists lists the users who have placed at least one order,
// then the users who have placed none, from one correlated subquery used twice.
func Example_rasql_exists() {
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
		{ID: 3, Email: "cyd@example.com"},
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
		{ID: 2, UserID: 3, Total: 100},
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

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", usersColumns.ID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", usersColumns.Email.Expr(), schema.TextType{}, ""),
	}, userSummaryDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// hasOrder reads the orders of the user the enclosing statement is on.
	// Correlated names that enclosing source first, because Where validates
	// the predicate it is given and no statement encloses this one yet.
	// EXISTS reads no value, so the projected column is arbitrary; a column of
	// the subquery's own table costs no parameter and renders the same on
	// every engine.
	ordersIDProjection, err := rasql.Scalar("id", ordersColumns.ID.Expr(), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to build the orders subquery projection: %s\n", err)
		return
	}
	hasOrder := rasql.Select(orders, ordersIDProjection).
		Correlated(users).
		Where(rasql.EqualExpr(ordersColumns.UserID.Expr(), usersColumns.ID.Expr()))

	exists, err := rasql.ExistsQuery(hasOrder)
	if err != nil {
		fmt.Printf("failed to build the exists predicate: %s\n", err)
		return
	}
	// SQL: SELECT users.id, users.email FROM users WHERE EXISTS (SELECT orders.id FROM orders WHERE orders.user_id = users.id) ORDER BY users.id ASC
	buyers, err := rasql.All(ctx, db,
		rasql.Select(users, projection).
			Where(exists).
			OrderBy(rasql.AscExpr(usersColumns.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users with an order: %s\n", err)
		return
	}
	for _, buyer := range buyers {
		fmt.Println("ordered:", buyer.ID, buyer.Email)
	}

	// The same subquery under NOT EXISTS answers the opposite question, and it
	// is still evaluated once per user rather than once for the statement.
	notExists, err := rasql.NotExistsQuery(hasOrder)
	if err != nil {
		fmt.Printf("failed to build the not-exists predicate: %s\n", err)
		return
	}
	quiet, err := rasql.All(ctx, db,
		rasql.Select(users, projection).
			Where(notExists).
			OrderBy(rasql.AscExpr(usersColumns.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users without an order: %s\n", err)
		return
	}
	for _, user := range quiet {
		fmt.Println("no order:", user.ID, user.Email)
	}

	// Output:
	// ordered: 1 ada@example.com
	// ordered: 3 cyd@example.com
	// no order: 2 bob@example.com
}
