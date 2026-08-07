package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_distinct() {
	// This example lists the users who have placed at least one order,
	// without repeating a user who placed more than one.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// A typed descriptor makes orders usable with rasql.Insert.
	type orderRow struct {
		ID     int `rasql:"id"`
		UserID int `rasql:"user_id"`
	}
	// A local result type holds one row per distinct user id.
	type orderingUser struct {
		UserID int64 `rasql:"user_id"`
	}
	orders := rasql.MustTable[orderRow](schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	if err := rasql.Create(ctx, client, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	for _, order := range []orderRow{
		{ID: 1, UserID: 1},
		{ID: 2, UserID: 2},
		{ID: 3, UserID: 1},
	} {
		if _, err := rasql.Insert(ctx, client, orders, order); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// orders has no generated column field for user_id, so it is looked up by
	// name. That lookup validates it against the descriptor as the query is
	// assembled.
	orderUserID, err := orders.Column("user_id")
	if err != nil {
		fmt.Printf("failed to find orders.user_id: %s\n", err)
		return
	}

	// Distinct is meaningful here because Project narrows the result to
	// user_id alone; SelectFrom would already select the orders primary key,
	// which makes every row unique before DISTINCT runs.
	rows, err := rasql.DecodeFrom[orderingUser](orders).
		Project(query.Project(orderUserID).As("user_id")).
		Distinct().
		Order(query.Asc(orderUserID)).
		Query(ctx, client)
	if err != nil {
		fmt.Printf("failed to query ordering users: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query ordering users: %s\n", err)
			return
		}
		fmt.Println(found.UserID)
	}

	// Output:
	// 1
	// 2
}
