package examples_test

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// UserRow is the Go type of one row. Each tag names the column that field
// holds. A generated row type has the same shape.
// BEGIN(row_type)

type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// END(row_type)

// UsersTable embeds the typed table and exposes one accessor method per
// column, so a mistyped column name fails to compile instead of failing at
// run time.
// BEGIN(table_type)

type UsersTable struct {
	rasql.Table[UserRow]
}

func (t UsersTable) ID() query.ColumnRef    { return rasql.ColumnOf(t.Table, "id") }
func (t UsersTable) Email() query.ColumnRef { return rasql.ColumnOf(t.Table, "email") }

// END(table_type)

// users keeps the row type and its table description together. The other
// examples use it as if it came from generated source.
// BEGIN(table_value)

var users = UsersTable{rasql.MustTableOf[UserRow](schema.MustTableDef("users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.PrimaryKey("id"),
))}

// END(table_value)

// As returns the table under alias.
func (t UsersTable) As(alias string) (UsersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return UsersTable{}, err
	}
	return UsersTable{Table: aliased}, nil
}

// OrderRow is one row of the orders table, which the join, projection, and
// subquery examples read beside users.
type OrderRow struct {
	ID     int64 `rasql:"id"`
	UserID int64 `rasql:"user_id"`
	Total  int64 `rasql:"total"`
}

// OrdersTable has the same shape as UsersTable: the typed table plus one
// accessor method per column.
type OrdersTable struct {
	rasql.Table[OrderRow]
}

func (t OrdersTable) ID() query.ColumnRef     { return rasql.ColumnOf(t.Table, "id") }
func (t OrdersTable) UserID() query.ColumnRef { return rasql.ColumnOf(t.Table, "user_id") }
func (t OrdersTable) Total() query.ColumnRef  { return rasql.ColumnOf(t.Table, "total") }

var orders = OrdersTable{rasql.MustTableOf[OrderRow](schema.MustTableDef("orders",
	schema.Integer("id"),
	schema.Integer("user_id"),
	schema.Integer("total"),
	schema.PrimaryKey("id"),
))}

// As returns the table under alias.
func (t OrdersTable) As(alias string) (OrdersTable, error) {
	aliased, err := rasql.As(t.Table, alias)
	if err != nil {
		return OrdersTable{}, err
	}
	return OrdersTable{Table: aliased}, nil
}
