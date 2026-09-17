package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

func Example_mutationBatch() {
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`INSERT INTO "users" \("email", "first_name", "last_name"\) VALUES \(\?, \?, \?\), \(\?, \?, \?\)`).
		WithArgs("ada@example.com", "Ada", "Lovelace", "grace@example.com", "Grace", "Hopper").
		WillReturnResult(sqlmock.NewResult(1, 2))
	executor, _ := rasql.Open(context.Background(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	// BEGIN(mutationBatch)
	first, err := store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
	if err != nil {
		fmt.Println(err)
		return
	}
	second, err := store.NewUsersCreate().Email("grace@example.com").FirstName("Grace").LastName("Hopper").Plan()
	if err != nil {
		fmt.Println(err)
		return
	}
	outcome, err := rasql.ExecMutationBatch(context.Background(), executor,
		[]rasql.MutationPlan{first, second}, rasql.BulkOptions{MaxRows: 100})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%v\n", outcome.Inputs)
	// END(mutationBatch)
	// Output: [1 1]
}
