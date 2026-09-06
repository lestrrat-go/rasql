package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite"
)

// Example_rasql_lifecycle_observer reports execution and row consumption as
// separate events, so a successful query does not imply successful scanning.
func Example_rasql_lifecycle_observer() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Println("open error")
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Println("new error")
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Println("table error")
		return
	}
	events := make([]string, 0, 2)
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, operation rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
			switch completion.Phase {
			case rasql.ExecutionPhase:
				events = append(events, "execution")
			case rasql.ConsumptionPhase:
				events = append(events, fmt.Sprintf("consumption rows=%d", completion.RowsRead))
			}
			return nil
		})
	}))
	if err != nil {
		fmt.Println("observer error")
		return
	}
	rows, err := rasql.SelectFrom(users).Query(ctx, db)
	if err != nil {
		fmt.Println("query error")
		return
	}
	for _, err := range rows {
		if err != nil {
			fmt.Println("scan error")
			return
		}
	}
	fmt.Println(events)

	// Output:
	// [execution consumption rows=0]
}
