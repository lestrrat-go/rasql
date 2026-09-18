package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

func Example_typedPatch() {
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`UPDATE "users" SET "status" = \? WHERE \("users"\."id" = \?\)`).
		WithArgs("active", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	executor, _ := rasql.Open(context.Background(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	columns, err := (store.UsersColumns{}).Bind(store.Users())
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, _ := store.Users().Patch().Status("active").Where(rasql.EqualValue(columns.ID.Expr(), int64(1)))
	_, err = rasql.ExecMutation(context.Background(), executor, plan)
	fmt.Println(err)
	// Output: <nil>
}
