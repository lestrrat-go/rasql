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

// Example_rasql_observer solves the case where telemetry fails after a database
// operation succeeds. The observer error goes to the extension error handler,
// while the caller still receives the successful write outcome.
func Example_rasql_observer() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	// Capture extension failures separately from operation failures so the
	// successful insert is not reported as failed.
	var reported error
	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Install a failing observer and a handler that records its error. The
	// executor keeps both policies attached to subsequent operations.
	executor, err := db.WithObservers(rasql.ExtensionErrorHandlerFunc(func(_ context.Context, extensionErr rasql.ExtensionError) {
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
	// Execute through the decorated executor so the observer runs after the
	// database reports the insert result.
	outcome, err := store.Users().Create().ID(1).Email("ada@example.com").Status("active").FirstName("First").LastName("Last").Exec(ctx, executor)
	if err != nil {
		fmt.Printf("insert failed: %s\n", err)
		return
	}
	fmt.Println("rows affected:", outcome.Affected)
	fmt.Println("exporter error:", reported)

	// Output:
	// rows affected: 1
	// exporter error: telemetry unavailable
}
