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

// UsersTable embeds the typed table and exposes one field per column, so a
// mistyped column name fails to compile instead of failing at run time.
type UsersTable struct {
	rasql.Table[UserRow]
	ID    query.Column
	Email query.Column
}

func newUsersTable(table rasql.Table[UserRow]) UsersTable {
	return UsersTable{
		Table: table,
		ID:    rasql.MustColumn(table, "id"),
		Email: rasql.MustColumn(table, "email"),
	}
}

// users keeps the generated row type and its column references together.
var users = newUsersTable(rasql.MustTableOf[UserRow](schema.MustTable("users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.PrimaryKey("id"),
)))

// As returns the table under alias, with every column rebound to it.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return newUsersTable(aliased), nil
}
