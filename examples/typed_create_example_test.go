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
	executor, _ := rasql.Open(context.Background(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	plan, err := store.Users().Create().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = rasql.Exec(context.Background(), executor, plan)
	fmt.Println(err)
	// Output: <nil>
}
