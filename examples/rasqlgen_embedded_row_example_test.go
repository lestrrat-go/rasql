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

// userWithRole embeds the generated row type rather than replacing it, which
// is the case this example exists to show: the embedded row keeps its own
// ScanRow method, promoted onto userWithRole, and Role rides along beside it.
type userWithRole struct {
	store.UsersRow // promotes ScanRow and ScanDestinations
	Role           string
}

// userWithRoleDecoder decodes through userWithRole's promoted
// ScanDestinations, which only the embedded UsersRow's own fields feed: Role
// keeps its zero value. ScanDestinations maps by column name, so a decoder
// projecting fewer columns than the whole table still resolves each one to
// the field it belongs to.
type userWithRoleDecoder struct {
	result  rasql.ResultSchema
	columns []string
}

func (d userWithRoleDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userWithRoleDecoder) Presence() []rasql.Presence       { return nil }
func (d userWithRoleDecoder) DecodeRow(src rasql.ScanSource, row *userWithRole) error {
	destinations, err := row.ScanDestinations(d.columns)
	if err != nil {
		return err
	}
	return src.Scan(destinations...)
}

// Example_rasqlgen_embedded_row shows what embedding a generated row type
// does to a read. The wrapper promotes the embedded row's ScanRow, the
// projection's decoder uses it, and it fills only the embedded fields, so
// Role keeps its zero value and nothing is reported.
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
	if err := rasql.CreateTable(ctx, db, store.Users()); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, store.NewUsersCreate().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Plan()); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	users := store.Users()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		fmt.Printf("failed to bind users source: %s\n", err)
		return
	}
	id, err := rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind id column: %s\n", err)
		return
	}
	email, err := rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind email column: %s\n", err)
		return
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", email.Expr(), schema.TextType{}, ""),
	}, userWithRoleDecoder{result: result, columns: []string{"id", "email"}})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	wrapped, err := rasql.One(ctx, executor, rasql.Select(source.Source(), projection))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Printf("%d %q role=%q\n", wrapped.ID, wrapped.Email, wrapped.Role)

	// Output:
	// 1 "ada@example.com" role=""
}
