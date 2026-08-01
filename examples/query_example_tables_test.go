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

// UsersTable embeds the typed table and adds one query.Column field per column.
// The query builders take those fields, so no column is named by a string.
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
var users = newUsersTable(rasql.MustTable[UserRow](schema.Table{
	Name: "users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	},
	PrimaryKey: []string{"id"},
}))

// As returns the table under alias, with every column rebound to it.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return newUsersTable(aliased), nil
}
