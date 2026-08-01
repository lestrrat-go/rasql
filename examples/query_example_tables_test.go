package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/query"
)

// users is application configuration created once during startup and reused by
// each query that addresses the users table.
var users = mustNewTableRef()

func mustNewTableRef() query.TableRef {
	reference, err := query.NewTableRef(usersTableDefinition())
	if err != nil {
		panic(fmt.Sprintf("create table reference: %s", err))
	}
	return reference
}
