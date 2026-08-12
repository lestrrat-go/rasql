package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// personRow writes a people row. Its rasql tags name the column each field
// holds, which is the mapping a generated row type states as Go code instead.
type personRow struct {
	ID        int64  `rasql:"id"`
	Email     string `rasql:"email"`
	FirstName string `rasql:"first_name"`
	LastName  string `rasql:"last_name"`
}

var people = rasql.MustTableOf[personRow](schema.MustTableDef("people",
	schema.Integer("id"),
	schema.Text("email"),
	schema.Text("first_name"),
	schema.Text("last_name"),
	schema.PrimaryKey("id"),
))

// BEGIN(computed_field)
type userReport struct {
	Email    string
	FullName string
}

func (r *userReport) DecodeRow(src row.Dynamic) error {
	if err := row.Assign(src, "email", &r.Email); err != nil {
		return err
	}
	var first, last string
	if err := row.Assign(src, "first_name", &first); err != nil {
		return err
	}
	if err := row.Assign(src, "last_name", &last); err != nil {
		return err
	}
	r.FullName = first + " " + last
	return nil
}

// END(computed_field)

// Example_rasqlgen_decode_row maps a result to a field no single column holds.
// A rasql tag can only name one column, so a value built from two of them is
// what DecodeRow is for.
func Example_rasqlgen_decode_row() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, people); err != nil {
		fmt.Printf("failed to create people table: %s\n", err)
		return
	}
	if _, err := rasql.Insert(ctx, db, people, personRow{ID: 1, Email: "ada@example.com", FirstName: "Ada", LastName: "Lovelace"}); err != nil {
		fmt.Printf("failed to insert person: %s\n", err)
		return
	}

	// DecodeFrom projects what the caller names, since the result shape is not
	// the table's row type.
	email, err := people.Column("email")
	if err != nil {
		fmt.Printf("failed to find people.email: %s\n", err)
		return
	}
	first, err := people.Column("first_name")
	if err != nil {
		fmt.Printf("failed to find people.first_name: %s\n", err)
		return
	}
	last, err := people.Column("last_name")
	if err != nil {
		fmt.Printf("failed to find people.last_name: %s\n", err)
		return
	}
	report, err := rasql.DecodeFrom[userReport](people).
		Project(query.Project(email), query.Project(first), query.Project(last)).
		One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query people: %s\n", err)
		return
	}
	fmt.Println(report.Email, report.FullName)

	// Output:
	// ada@example.com Ada Lovelace
}

// BEGIN(embedded_row)
type userWithRole struct {
	store.UsersRow // promotes DecodeRow
	Role           string
}

// END(embedded_row)

// Example_rasqlgen_embedded_row shows what embedding a generated row type does
// to decoding. A generated row carries the scan methods rasqlgen emits, and the
// wrapper promotes those too, so the result is filled through the embedded row
// and Role keeps its zero value without anything being reported.
func Example_rasqlgen_embedded_row() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, store.Users()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := rasql.Insert(ctx, db, store.Users(), store.UsersRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	users := store.Users()
	wrapped, err := rasql.DecodeFrom[userWithRole](users).
		Project(query.Project(users.ID), query.Project(users.Email)).
		One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Printf("%d %q role=%q\n", wrapped.ID, wrapped.Email, wrapped.Role)

	// Output:
	// 1 "ada@example.com" role=""
}
