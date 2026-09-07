package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

func Example_typedCreate() {
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`INSERT INTO "users" \("email", "first_name", "last_name"\) VALUES \(\?, \?, \?\)`).
		WithArgs("ada@example.com", "Ada", "Lovelace").
		WillReturnResult(sqlmock.NewResult(1, 1))
	db, _ := rasql.New(database, dialect.SQLite())
	_, err := rasql.ExecCreate(context.Background(), db, store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan())
	fmt.Println(err)
	// Output: <nil>
}
