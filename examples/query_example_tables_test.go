package examples_test

import (
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// users has the same shape as a table reference emitted by rasqlgen.
var users = query.MustNewTableRef(schema.Table{
	Name: "users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
})
