package examples

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

type relationshipExampleUser struct{ ID int64 }
type relationshipExampleOrder struct {
	ID        int64
	UserID    int64
	Active    int64
	CreatedAt int64
}

// ExampleLoadHasManyPlan demonstrates a filtered, ordered, capped relationship load.
func ExampleLoadHasManyPlan() {
	// BEGIN(relationship_load)
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, active INTEGER, created_at INTEGER); INSERT INTO orders VALUES (1,1,1,1),(2,1,1,2),(3,1,0,3),(4,1,1,4),(5,1,1,5),(6,1,1,6),(7,1,1,7)"); err != nil {
		panic(err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		panic(err)
	}
	orders, err := rasql.TableOf[relationshipExampleOrder](schema.TableDef{
		Name:       "orders",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}}, {Name: "active", Type: schema.IntegerType{}}, {Name: "created_at", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	if err != nil {
		panic(err)
	}
	loaded, err := rasql.LoadHasManyPlan(context.Background(), db, orders, []query.ColumnRef{orders.Column("user_id")}, []relationshipExampleUser{{ID: 1}}, func(user relationshipExampleUser) int64 { return user.ID }, func(order relationshipExampleOrder) int64 { return order.UserID }, func(key int64) ([]any, bool) { return []any{key}, true }, rasql.RelationshipLoadOptions{
		Where:          query.Equal(orders.Column("active"), 1),
		OrderBy:        []query.Order{query.Desc(orders.Column("created_at")), query.Asc(orders.Column("id"))},
		PerParentLimit: 5,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(len(loaded[1]), loaded[1][0].ID, loaded[1][4].ID)
	// END(relationship_load)
	// Output: 5 7 2
}
