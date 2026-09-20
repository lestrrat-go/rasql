package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

// Example_mutationBatch solves the cost of sending many compatible inserts as
// separate statements. It builds ordinary typed mutation plans, then lets
// ExecBatch combine them into one multi-row insert while retaining one outcome
// entry per input plan.
func Example_mutationBatch() {
	// The mock makes the one-statement boundary visible: two input plans must
	// produce one INSERT with two value groups.
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`INSERT INTO "users" \("email", "first_name", "last_name"\) VALUES \(\?, \?, \?\), \(\?, \?, \?\)`).
		WithArgs("ada@example.com", "Ada", "Lovelace", "grace@example.com", "Grace", "Hopper").
		WillReturnResult(sqlmock.NewResult(1, 2))
	// A fixed profile avoids a version-discovery query that is unrelated to
	// the batching behavior under test.
	executor, _ := rasql.Open(context.Background(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	// BEGIN(mutationBatch)
	// Plan stops before execution, which gives ExecBatch the mutation shapes it
	// needs to check and combine.
	first, err := store.Users().Create().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
	if err != nil {
		fmt.Println(err)
		return
	}
	second, err := store.Users().Create().Email("grace@example.com").FirstName("Grace").LastName("Hopper").Plan()
	if err != nil {
		fmt.Println(err)
		return
	}
	// MaxRows bounds the number of input rows rasql may place in one statement.
	outcome, err := rasql.ExecBatch(context.Background(), executor,
		[]rasql.MutationPlan{first, second}, rasql.BulkOptions{MaxRows: 100})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%v\n", outcome.Inputs)
	// END(mutationBatch)
	// Output: [1 1]
}
