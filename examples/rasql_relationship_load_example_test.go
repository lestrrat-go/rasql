package examples

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite"
)

// ExampleLoadHasManyPlan demonstrates a filtered, ordered, capped relationship load.
func ExampleLoadHasManyPlan() {
	// BEGIN(relationship_load)
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, total INTEGER); INSERT INTO orders VALUES (1,1,10),(2,1,20),(3,1,30),(4,1,40),(5,1,50),(6,1,60),(7,1,70)"); err != nil {
		panic(err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		panic(err)
	}
	orders := store.Orders()
	loaded, err := rasql.LoadHasManyPlan(context.Background(), db, orders, []query.ColumnRef{orders.UserID()}, []store.UsersRow{{ID: 1}}, func(user store.UsersRow) int64 { return user.ID }, func(order store.OrdersRow) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{
		Where:          query.Equal(orders.UserID(), 1),
		OrderBy:        []query.Order{query.Desc(orders.ID())},
		PerParentLimit: 5,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(len(loaded[1]), loaded[1][0].ID, loaded[1][4].ID)
	// END(relationship_load)
	// Output: 5 7 2
}
