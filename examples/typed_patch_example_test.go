package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
)

func Example_typedPatch() {
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`UPDATE "users" SET "status" = \? WHERE \("users"\."id" = \?\)`).
		WithArgs("active", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	db, _ := rasql.New(database, dialect.SQLite())
	predicate := query.EqualValue(store.Users().ID(), int64(1))
	plan, _ := store.NewUsersPatch().Status("active").Where(predicate)
	_, err := rasql.ExecPatch(context.Background(), db, plan)
	fmt.Println(err)
	// Output: <nil>
}
