package examples_test

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/dberror"
	"github.com/lestrrat-go/rasql/dberror/sqliteerr"
	"modernc.org/sqlite"
)

// Example_dberror_classify shows a portable category alongside access to the
// original driver error when an application needs native details.
func Example_dberror_classify() {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(`CREATE TABLE users (email TEXT UNIQUE); INSERT INTO users VALUES ('ada@example.com')`); err != nil {
		fmt.Println(err)
		return
	}
	_, err = database.Exec(`INSERT INTO users VALUES ('ada@example.com')`)
	metadata, ok := dberror.Classify(err, sqliteerr.New())
	if !ok {
		fmt.Println("unclassified")
		return
	}
	var native *sqlite.Error
	fmt.Println(metadata.Category, metadata.NativeCode, errors.As(err, &native))

	// Output: unique_violation 2067 true
}
