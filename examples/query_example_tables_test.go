package examples_test

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// UserRow, UsersTable, and users have the shape rasqlgen creates for a table
// definition. The other examples use them as if they came from generated source.
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// UsersTable embeds the typed table and exposes one accessor method per
// column, so a mistyped column name fails to compile instead of failing at
// run time.
type UsersTable struct {
	rasql.Table[UserRow]
}

func (t UsersTable) ID() query.ColumnRef    { return rasql.ColumnOf(t.Table, "id") }
func (t UsersTable) Email() query.ColumnRef { return rasql.ColumnOf(t.Table, "email") }

// users keeps the generated row type and its table value together.
var users = UsersTable{rasql.MustTableOf[UserRow](schema.MustTableDef("users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.PrimaryKey("id"),
))}

// As returns the table under alias.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return UsersTable{Table: aliased}, nil
}
