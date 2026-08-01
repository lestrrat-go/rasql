package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// users is application configuration created once during startup and reused by
// each query that addresses the users table.
var users = mustNewTableRef(schema.Table{
	Name: "users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
})

func mustNewTableRef(table schema.Table) query.TableRef {
	reference, err := query.NewTableRef(table)
	if err != nil {
		panic(fmt.Sprintf("create table reference: %s", err))
	}
	return reference
}
