package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
)

type userEmail struct {
	Email string `rasql:"email"`
}

func Example_rebindTypedResult() {
	users := store.Users()
	base := rasql.SelectFrom(users).WhereEqual(users.ID(), 7)
	result, err := base.Result()
	if err != nil {
		return
	}
	_ = result
	dto := rasql.RebindResult[userEmail](base,
		[]query.ResultColumn{{Name: "email", Type: users.Ref().Definition().Columns[1].Type}},
		users.Email(),
	)
	statement, err := dto.Build(dialect.PostgreSQL())
	if err != nil {
		return
	}
	fmt.Println(statement.SQL())
	// Output:
	// SELECT "users"."email" FROM "users" WHERE ("users"."id" = $1)
}
