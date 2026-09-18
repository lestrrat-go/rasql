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
	store.UsersRow // promotes ScanRow
	Role           string
}

// userWithRoleDecoder decodes through userWithRole's promoted ScanRow, which
// fills the embedded UsersRow's own fields and nothing else: Role keeps its
// zero value. ScanRow reads the result columns in the order the generated row
// declares them, so the projection below names all six in that order.
type userWithRoleDecoder struct{ result rasql.ResultSchema }

func (d userWithRoleDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userWithRoleDecoder) Presence() []rasql.Presence       { return nil }
func (d userWithRoleDecoder) DecodeRow(src rasql.ScanSource, row *userWithRole) error {
	return row.ScanRow(src)
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

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	plan, err := store.Users().Create().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	if _, err := rasql.Exec(ctx, db, plan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The generated columns are reused, but not the generated projection:
	// store.UsersProjection decodes into store.UsersRow, and this query
	// decodes into the wrapper instead, so it states its own decoder.
	columns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		rasql.ResultColumn{Name: "status", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "first_name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "last_name", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", columns.ID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", columns.Email.Expr(), schema.TextType{}, ""),
		rasql.NullItem("nickname", columns.Nickname.NullExpr(), schema.TextType{}, ""),
		rasql.Item("status", columns.Status.Expr(), schema.TextType{}, ""),
		rasql.Item("first_name", columns.FirstName.Expr(), schema.TextType{}, ""),
		rasql.Item("last_name", columns.LastName.Expr(), schema.TextType{}, ""),
	}, userWithRoleDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	wrapped, err := rasql.One(ctx, db, rasql.Select(users, projection))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Printf("%d %q role=%q\n", wrapped.ID, wrapped.Email, wrapped.Role)

	// Output:
	// 1 "ada@example.com" role=""
}
