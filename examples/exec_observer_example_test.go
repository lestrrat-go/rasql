package examples_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite"
)

// Example_rasql_observer keeps a successful write result available while
// reporting an exporter failure through the configured extension handler.
func Example_rasql_observer() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	var reported error
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	db, err = db.WithObservers(rasql.ExtensionErrorHandlerFunc(func(_ context.Context, extensionErr rasql.ExtensionError) {
		reported = extensionErr.Errors[0]
	}), rasql.ObserverFunc(func(context.Context, rasql.Operation, error) error {
		return errors.New("telemetry unavailable")
	}))
	if err != nil {
		fmt.Printf("failed to install observer: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	result, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: 1, Email: "ada@example.com", Status: "active"})
	if err != nil {
		fmt.Printf("insert failed: %s\n", err)
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to read result: %s\n", err)
		return
	}
	fmt.Println("rows affected:", rows)
	fmt.Println("exporter error:", reported)

	// Output:
	// rows affected: 1
	// exporter error: telemetry unavailable
}
