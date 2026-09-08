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

// userDisplayName holds the decoded id and the aliased display name.
// DisplayName falls back from nickname to email with CoalesceExpr, so it is
// never NULL even though nickname, the column it is drawn from, is.
type userDisplayName struct {
	ID          int64
	DisplayName string
}

type userDisplayNameDecoder struct{ result rasql.ResultSchema }

func (d userDisplayNameDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userDisplayNameDecoder) Presence() []rasql.Presence       { return nil }
func (d userDisplayNameDecoder) DecodeRow(src rasql.ScanSource, row *userDisplayName) error {
	return src.Scan(&row.ID, &row.DisplayName)
}

// Example_rasql_order_by_alias binds a projection item to a variable once and
// passes that same variable to both the projection and OrderBy, so the ORDER
// BY reads the projection's already-computed result instead of repeating its
// expression, and renaming its alias can never drift the two apart the way
// writing the alias out as a second string could.
func Example_rasql_order_by_alias() {
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

	nick := "Ada"
	if _, err := rasql.ExecMutation(ctx, executor, store.NewUsersCreate().ID(1).Email("ada@example.com").Nickname(&nick).FirstName("First").LastName("Last").Plan()); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, store.NewUsersCreate().ID(2).Email("bob@example.com").FirstName("First").LastName("Last").Plan()); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	source, err := rasql.SourceOf(users, "")
	if err != nil {
		fmt.Printf("failed to bind users source: %s\n", err)
		return
	}
	id, err := rasql.BindTypedColumn(users.ID())
	if err != nil {
		fmt.Printf("failed to bind id column: %s\n", err)
		return
	}
	// email and nickname are both bound by column name rather than through
	// users.Email() and users.Nickname(): CoalesceExpr requires the same Go
	// type on its nullable value and its fallback, but a generated accessor
	// always mirrors the row's own field type, string for email and *string
	// for the nullable nickname. Binding both explicitly as string is what
	// gives CoalesceExpr a matching pair; no accessor bridges that gap.
	email, err := rasql.BindColumn[store.UsersRow, string](source, "email", "")
	if err != nil {
		fmt.Printf("failed to bind email column: %s\n", err)
		return
	}
	nickname, err := rasql.BindNullColumn[store.UsersRow, string](source, "nickname", "")
	if err != nil {
		fmt.Printf("failed to bind nickname column: %s\n", err)
		return
	}

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "display_name", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	// displayName is written once and used in both the projection and the
	// OrderBy below. CoalesceExpr falls back to email whenever nickname is
	// NULL, so display_name is never NULL even though nickname is.
	displayNameValue := rasql.CoalesceExpr(nickname.NullExpr(), email.Expr())
	displayName := rasql.Item("display_name", displayNameValue, schema.TextType{}, "")
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		displayName,
	}, userDisplayNameDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	statement, err := rasql.Render(
		rasql.Select(source.Source(), projection).OrderBy(rasql.DescResult(displayName)),
		dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	rows, err := rasql.All(ctx, executor,
		rasql.Select(source.Source(), projection).OrderBy(rasql.DescResult(displayName)))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range rows {
		fmt.Println(user.ID, user.DisplayName)
	}

	// A second query would order by a result name two projected items report:
	// id is projected without a wrapper, and email is separately aliased id.
	// rasql refuses this before the projection can even be built, since
	// PostgreSQL and MySQL both call two results with the same name
	// ambiguous and SQLite would otherwise resolve it silently.
	emailAsID := rasql.Item("id", email.Expr(), schema.TextType{}, "")
	_, ambiguousErr := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		emailAsID,
	}, userDisplayNameDecoder{result: result})
	if ambiguousErr != nil {
		fmt.Println(ambiguousErr)
	}

	// Output:
	// SELECT "users"."id" AS "id", COALESCE("users"."nickname", "users"."email") AS "display_name" FROM "users" ORDER BY "display_name" DESC
	// 2 bob@example.com
	// 1 Ada
	// invalid_schema at columns[1].name: duplicates id
}
