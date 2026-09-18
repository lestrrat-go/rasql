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

	withNickname, err := store.Users().Create().ID(1).Email("Ada@Example.com").Nickname("Ada").FirstName("First").LastName("Last").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	if _, err := rasql.Exec(ctx, db, withNickname); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	withoutNickname, err := store.Users().Create().ID(2).Email("bob@example.com").FirstName("First").LastName("Last").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	if _, err := rasql.Exec(ctx, db, withoutNickname); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	columns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
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
	name := rasql.CoalesceExpr(columns.Nickname.NullExpr(), columns.Email.Expr())
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", columns.ID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("name", name, schema.TextType{}, ""),
	}, userNameDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}
	base := rasql.Select(users, projection)

	// LowerExpr matches "Ada@Example.com" against the lower-case literal a
	// caller would type, regardless of how the stored value was cased.
	byEmailQuery := base.Where(rasql.EqualValue(rasql.LowerExpr(columns.Email.Expr()), "ada@example.com"))
	byEmailStatement, err := rasql.Render(byEmailQuery, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(byEmailStatement.SQL())
	fmt.Println(byEmailStatement.Args())
	byEmail, err := rasql.All(ctx, db, byEmailQuery)
	if err != nil {
		fmt.Printf("failed to query user by email: %s\n", err)
		return
	}
	for _, user := range byEmail {
		fmt.Println(user.Name)
	}

	// COALESCE(nickname, email) reads every user's display name, falling
	// back to the email once nickname is NULL.
	namesQuery := base.OrderBy(rasql.AscExpr(columns.ID.Expr()))
	namesStatement, err := rasql.Render(namesQuery, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(namesStatement.SQL())
	names, err := rasql.All(ctx, db, namesQuery)
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
