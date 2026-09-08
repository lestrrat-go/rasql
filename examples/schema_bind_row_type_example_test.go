package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// userRowDecoder decodes a UserRow from its two columns, in projection order.
type userRowDecoder struct{ result rasql.ResultSchema }

func (d userRowDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userRowDecoder) Presence() []rasql.Presence       { return nil }
func (d userRowDecoder) DecodeRow(src rasql.ScanSource, row *UserRow) error {
	return src.Scan(&row.ID, &row.Email)
}

// UserRow maps the "users" table this example declares. It is declared
// package-level, rather than inside the example, only so its methods can be
// promoted to userRowDecoder's row-type parameter; the marked region below is
// what a reader copies.
type UserRow struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// Example_schema_bind_row_type pairs a table description with the Go type of
// one of its rows, which is the binding rasqlgen performs for a generated
// table. The row type is written out here so the example stands on its own;
// the other examples read the tables generated into examples/store.
func Example_schema_bind_row_type() {
	ctx := context.Background()
	definition := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)

	// BEGIN(bind_row_type)
	users := rasql.MustTableOf[UserRow](definition)
	// END(bind_row_type)

	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
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
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	source, err := rasql.SourceOf(users, "")
	if err != nil {
		fmt.Printf("failed to bind users source: %s\n", err)
		return
	}
	id, err := rasql.BindColumn[UserRow, int64](source, "id", "")
	if err != nil {
		fmt.Printf("failed to bind id column: %s\n", err)
		return
	}
	email, err := rasql.BindColumn[UserRow, string](source, "email", "")
	if err != nil {
		fmt.Printf("failed to bind email column: %s\n", err)
		return
	}

	createPlan, err := rasql.NewCreatePlan[UserRow](users,
		rasql.SetField[UserRow](id, int64(1)),
		rasql.SetField[UserRow](email, "ada@example.com"),
	)
	if err != nil {
		fmt.Printf("failed to build create plan: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, createPlan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
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
	}, userRowDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// The bound table is what the typed API takes, so a select from it
	// already knows it returns a UserRow.
	user, err := rasql.One(ctx, executor, rasql.Select(source.Source(), projection))
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(user.ID, user.Email)

	// Output:
	// 1 ada@example.com
}
