package examples_test

import (
	"context"
	"fmt"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
)

// Example_typedPatch solves a partial update without sending unchanged fields.
// The generated patch builder records only status, and the typed predicate
// limits the update to one user.
func Example_typedPatch() {
	// The mock verifies the patch contains one assignment and keeps the id as a
	// separate bound predicate value.
	database, mock, _ := sqlmock.New()
	defer func() { _ = database.Close() }()
	mock.ExpectExec(`UPDATE "users" SET "status" = \? WHERE \("users"\."id" = \?\)`).
		WithArgs("active", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Supply the profile because a mock cannot answer rasql's normal SQLite
	// version-discovery query.
	executor, _ := rasql.Open(context.Background(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	// Status selects the only written column, while Where makes the target row
	// explicit before Exec is allowed to run the mutation.
	_, err := store.Users().Patch().Status("active").Where(rasql.EqualValue(store.Users().ID.Expr(), int64(1))).Exec(context.Background(), executor)
	fmt.Println(err)
	// Output: <nil>
}
