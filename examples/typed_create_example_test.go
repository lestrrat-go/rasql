package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

// Example_typedCreate solves a one-row insert without manually constructing
// assignments. The generated builder exposes only users columns, requires the
// configured fields, and binds their values before execution.
func Example_typedCreate() {
	// The mock verifies the builder emits one parameterized insert with values
	// in generated column order.
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`INSERT INTO "users" \("email", "first_name", "last_name"\) VALUES \(\?, \?, \?\)`).
		WithArgs("ada@example.com", "Ada", "Lovelace").
		WillReturnResult(sqlmock.NewResult(1, 1))
	// Supply the profile because a mock cannot answer rasql's normal SQLite
	// version-discovery query.
	executor, _ := rasql.Open(context.Background(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	// Chained field methods record only data; Exec plans, renders, and runs the
	// insert through the configured executor.
	_, err := store.Users().Create().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Exec(context.Background(), executor)
	fmt.Println(err)
	// Output: <nil>
}
