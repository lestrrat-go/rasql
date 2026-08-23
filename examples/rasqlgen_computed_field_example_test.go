package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// PersonRow writes a people row. Its rasql tags name the column each field
// holds, which is the mapping a generated row type states as Go code instead.
type PersonRow struct {
	ID        int64  `rasql:"id"`
	Email     string `rasql:"email"`
	FirstName string `rasql:"first_name"`
	LastName  string `rasql:"last_name"`
}

// PeopleTable has the shape rasqlgen emits: the typed table plus one accessor
// method per column.
type PeopleTable struct {
	rasql.Table[PersonRow]
}

func (t PeopleTable) ID() query.ColumnRef        { return rasql.ColumnOf(t.Table, "id") }
func (t PeopleTable) Email() query.ColumnRef     { return rasql.ColumnOf(t.Table, "email") }
func (t PeopleTable) FirstName() query.ColumnRef { return rasql.ColumnOf(t.Table, "first_name") }
func (t PeopleTable) LastName() query.ColumnRef  { return rasql.ColumnOf(t.Table, "last_name") }

var people = PeopleTable{rasql.MustTableOf[PersonRow](schema.MustTableDef("people",
	schema.Integer("id"),
	schema.Text("email"),
	schema.Text("first_name"),
	schema.Text("last_name"),
	schema.PrimaryKey("id"),
))}

type userReport struct {
	Email     string
	FirstName string `rasql:"first_name"`
	LastName  string `rasql:"last_name"`
}

func (r userReport) FullName() string {
	return r.FirstName + " " + r.LastName
}

// Example_rasqlgen_computed_field builds a value no single column holds. The
// raw columns stay as fields and the derived value is a method, so the
// mapping stays a plain field-to-column mapping.
func Example_rasqlgen_computed_field() {
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
	if _, err := rasql.Insert(ctx, db, people, PersonRow{ID: 1, Email: "ada@example.com", FirstName: "Ada", LastName: "Lovelace"}); err != nil {
		fmt.Printf("failed to insert person: %s\n", err)
		return
	}

	// DecodeFrom projects what the caller names, since the result shape is not
	// the table's row type.
	report, err := rasql.DecodeFrom[userReport](people).
		Project(people.Email(), people.FirstName(), people.LastName()).
		One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query people: %s\n", err)
		return
	}
	fmt.Println(report.Email, report.FullName())

	// Output:
	// ada@example.com Ada Lovelace
}
