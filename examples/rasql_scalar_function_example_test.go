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

// userName holds the decoded id and display name. name is COALESCE(nickname,
// email) under an alias rather than a stored column, so store.UsersRow has no
// field it could land in.
type userName struct {
	ID   int64
	Name string
}

type userNameDecoder struct{ result rasql.ResultSchema }

func (d userNameDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userNameDecoder) Presence() []rasql.Presence       { return nil }
func (d userNameDecoder) DecodeRow(src rasql.ScanSource, row *userName) error {
	return src.Scan(&row.ID, &row.Name)
}

// Example_rasql_scalar_function looks a user up by email regardless of case
// with LowerExpr, then reads every user's display name, falling back to their
// email with CoalesceExpr when no nickname is set. nickname is the users
// column declared nullable, which is what gives CoalesceExpr a real NULL to
// fall back from.
func Example_rasql_scalar_function() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
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
	if _, err := rasql.ExecMutation(ctx, executor, store.NewUsersCreate().ID(1).Email("Ada@Example.com").Nickname(&nick).FirstName("First").LastName("Last").Plan()); err != nil {
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
		rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	// name falls back from nickname to email with CoalesceExpr, so it is
	// never NULL even though nickname, the column it is drawn from, is.
	name := rasql.CoalesceExpr(nickname.NullExpr(), email.Expr())
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("name", name, schema.TextType{}, ""),
	}, userNameDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}
	base := rasql.Select(source.Source(), projection)

	// LowerExpr matches "Ada@Example.com" against the lower-case literal a
	// caller would type, regardless of how the stored value was cased.
	byEmailQuery := base.Where(rasql.EqualValue(rasql.LowerExpr(email.Expr()), "ada@example.com"))
	byEmailStatement, err := rasql.Render(byEmailQuery, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(byEmailStatement.SQL())
	fmt.Println(byEmailStatement.Args())
	byEmail, err := rasql.All(ctx, executor, byEmailQuery)
	if err != nil {
		fmt.Printf("failed to query user by email: %s\n", err)
		return
	}
	for _, user := range byEmail {
		fmt.Println(user.Name)
	}

	// COALESCE(nickname, email) reads every user's display name, falling
	// back to the email once nickname is NULL.
	namesQuery := base.OrderBy(rasql.AscExpr(id.Expr()))
	namesStatement, err := rasql.Render(namesQuery, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(namesStatement.SQL())
	names, err := rasql.All(ctx, executor, namesQuery)
	if err != nil {
		fmt.Printf("failed to query user names: %s\n", err)
		return
	}
	for _, user := range names {
		fmt.Println(user.ID, user.Name)
	}

	// Output:
	// SELECT "users"."id" AS "id", COALESCE("users"."nickname", "users"."email") AS "name" FROM "users" WHERE (LOWER("users"."email") = ?)
	// [ada@example.com]
	// Ada
	// SELECT "users"."id" AS "id", COALESCE("users"."nickname", "users"."email") AS "name" FROM "users" ORDER BY "users"."id"
	// 1 Ada
	// 2 bob@example.com
}
