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
	ID     query.ColumnRef
	Email  query.ColumnRef
	Status query.ColumnRef
}

func newDefaultUsersTable(table rasql.Table[defaultUserRow]) defaultUsersTable {
	return defaultUsersTable{
		Table:  table,
		ID:     rasql.MustColumn(table, "id"),
		Email:  rasql.MustColumn(table, "email"),
		Status: rasql.MustColumn(table, "status"),
	}
}

var defaultUsers = newDefaultUsersTable(rasql.MustTableOf[defaultUserRow](schema.MustTableDef("default_users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.Text("status", schema.Default("'pending'")),
	schema.PrimaryKey("id"),
)))

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
	if err := rasql.CreateTable(ctx, client, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// Name each database-assigned column. Email remains an explicit empty string.
	// SQL: INSERT INTO default_users (email) VALUES (?) (argument: "")
	if _, err := rasql.InsertWithOptions(ctx, client, defaultUsers, defaultUserRow{}, rasql.DefaultColumns("id", "status")); err != nil {
		fmt.Printf("failed to insert default user: %s\n", err)
		return
	}

	// SQL: SELECT default_users.id, default_users.email, default_users.status FROM default_users WHERE default_users.id = ? (argument: 1)
	user, err := rasql.SelectFrom(defaultUsers).WhereEqual(defaultUsers.ID, 1).One(ctx, client)
	if err != nil {
		fmt.Printf("failed to query default user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "" "pending"
}
