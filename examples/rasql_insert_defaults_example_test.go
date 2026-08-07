package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// defaultUserRow and defaultUsersTable have the method-based shape rasqlgen
// emits for a table with a generated ID and a defaulted status.
type defaultUserRow struct {
	ID     int64
	Email  string
	Status string
}

func (r *defaultUserRow) DecodeRow(source row.Dynamic) error {
	if err := row.Assign(source, "id", &r.ID); err != nil {
		return err
	}
	if err := row.Assign(source, "email", &r.Email); err != nil {
		return err
	}
	return row.Assign(source, "status", &r.Status)
}

func (r defaultUserRow) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return r.ID, true
	case "email":
		return r.Email, true
	case "status":
		return r.Status, true
	}
	return nil, false
}

type defaultUsersTable struct {
	rasql.Table[defaultUserRow]
	ID     query.Column
	Email  query.Column
	Status query.Column
}

func newDefaultUsersTable(table rasql.Table[defaultUserRow]) defaultUsersTable {
	return defaultUsersTable{
		Table:  table,
		ID:     rasql.MustColumn(table, "id"),
		Email:  rasql.MustColumn(table, "email"),
		Status: rasql.MustColumn(table, "status"),
	}
}

var defaultUsers = newDefaultUsersTable(rasql.MustTable[defaultUserRow](schema.Table{
	Name: "default_users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
		{Name: "status", Type: schema.TypeText, Default: "'pending'"},
	},
	PrimaryKey: []string{"id"},
}))

func Example_rasql_insert_defaults() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	if err := rasql.Create(ctx, client, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// Name each database-assigned column. Email remains an explicit empty string.
	if _, err := rasql.InsertWithOptions(ctx, client, defaultUsers, defaultUserRow{}, rasql.DefaultColumns("id", "status")); err != nil {
		fmt.Printf("failed to insert default user: %s\n", err)
		return
	}

	user, err := rasql.SelectFrom(defaultUsers).WhereEqual(defaultUsers.ID, 1).One(ctx, client)
	if err != nil {
		fmt.Printf("failed to query default user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "" "pending"
}
