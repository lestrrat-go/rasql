package examples_test

import (
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/runtime"
	"github.com/lestrrat-go/rasql/schema"
)

// UserRow and users have the same shape as values emitted by rasqlgen.
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

var users = runtime.MustTable[UserRow](query.MustNewTableRef(schema.Table{
	Name: "users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
}))
