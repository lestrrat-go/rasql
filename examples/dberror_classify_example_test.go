package examples_test

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/sqliteerr"
	"modernc.org/sqlite"
)

// Example_dberror_classify solves the need to branch on a database error across
// drivers without losing driver-specific details. Classify adds a portable
// category and native code while leaving the original error available to
// errors.As.
func Example_dberror_classify() {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = database.Close() }()
	// Seed a UNIQUE value so the next insert produces the SQLite error this
	// example needs to classify.
	if _, err := database.Exec(`CREATE TABLE users (email TEXT UNIQUE); INSERT INTO users VALUES ('ada@example.com')`); err != nil {
		fmt.Println(err)
		return
	}
	_, err = database.Exec(`INSERT INTO users VALUES ('ada@example.com')`)
	// The SQLite classifier translates the driver's native code into rasql's
	// engine-independent unique_violation category.
	metadata, ok := dberror.Classify(err, sqliteerr.New())
	if !ok {
		fmt.Println("unclassified")
		return
	}
	// Classification does not wrap or replace err, so callers may still inspect
	// the concrete driver error when the portable metadata is not enough.
	var native *sqlite.Error
	fmt.Println(metadata.Category, metadata.NativeCode, errors.As(err, &native))

	// Output: unique_violation 2067 true
}
