package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// userReport is a local result type rather than store.UsersRow, because a
// generated row type holds one field per column and has nowhere to put a
// value derived from two of them. FullName below is that value.
type userReport struct {
	Email     string
	FirstName string
	LastName  string
}

func (r userReport) FullName() string {
	return r.FirstName + " " + r.LastName
}

type userReportDecoder struct{ result rasql.ResultSchema }

func (d userReportDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userReportDecoder) Presence() []rasql.Presence       { return nil }
func (d userReportDecoder) DecodeRow(src rasql.ScanSource, row *userReport) error {
	return src.Scan(&row.Email, &row.FirstName, &row.LastName)
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
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Printf("failed to describe engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	plan := store.NewUsersCreate().ID(1).Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
	if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	source, err := rasql.SourceOf(users, "")
	if err != nil {
		fmt.Printf("failed to bind users source: %s\n", err)
		return
	}
	email, err := rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind email column: %s\n", err)
		return
	}
	firstName, err := rasql.BindColumn[store.UsersRow, string](source, users.FirstNameRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind first_name column: %s\n", err)
		return
	}
	lastName, err := rasql.BindColumn[store.UsersRow, string](source, users.LastNameRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind last_name column: %s\n", err)
		return
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "first_name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "last_name", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	// The projection names what the caller wants, since the result shape is
	// not the table's row type.
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("email", email.Expr(), schema.TextType{}, ""),
		rasql.Item("first_name", firstName.Expr(), schema.TextType{}, ""),
		rasql.Item("last_name", lastName.Expr(), schema.TextType{}, ""),
	}, userReportDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	report, err := rasql.One(ctx, executor, rasql.Select(source.Source(), projection))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Println(report.Email, report.FullName())

	// Output:
	// ada@example.com Ada Lovelace
}
