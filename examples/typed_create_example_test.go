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
	profile, _ := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	executor, _ := rasql.AsExecutor(db, profile)
	_, err := rasql.ExecMutation(context.Background(), executor, store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan())
	fmt.Println(err)
	// Output: <nil>
}
