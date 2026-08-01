package examples_test

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
)

// UserRow and users have the same shape as values emitted by rasqlgen.
// UserRow and users have the shape rasqlgen creates for a table definition.
// The other examples use them as if they came from generated source.
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// users keeps the generated row type and reusable query reference together.
var users = rasql.MustTable[UserRow](schema.Table{
	Name: "users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
})
