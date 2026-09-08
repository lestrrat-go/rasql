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

// userNickname holds the decoded id and a possibly-NULL nickname. The typed
// operator set has no COALESCE wrapper and no entry point for a hand-built
// query.Expression, so this example reads the nullable column directly
// rather than a nickname-falls-back-to-email expression.
type userNickname struct {
	ID       int64
	Nickname rasql.Nullable[string]
}

type userNicknameDecoder struct{ result rasql.ResultSchema }

func (d userNicknameDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d userNicknameDecoder) Presence() []rasql.Presence       { return nil }
func (d userNicknameDecoder) DecodeRow(src rasql.ScanSource, row *userNickname) error {
	return src.Scan(&row.ID, &row.Nickname)
}

// Example_rasql_scalar_function looks a user up by email regardless of case
// with LowerExpr, then reads every user's id and nickname in id order.
// nickname is the users column declared nullable.
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
	nickname, err := rasql.BindNullColumn[store.UsersRow, string](source, users.NicknameRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind nickname column: %s\n", err)
		return
	}

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.NullItem("nickname", nickname.NullExpr(), schema.TextType{}, ""),
	}, userNicknameDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}
	base := rasql.Select(source.Source(), projection)

	// LowerExpr matches "Ada@Example.com" against the lower-case literal a
	// caller would type, regardless of how the stored value was cased.
	// SQL: SELECT users.id, users.nickname FROM users WHERE LOWER(users.email) = ? (argument: "ada@example.com")
	byEmail, err := rasql.All(ctx, executor,
		base.Where(rasql.EqualValue(rasql.LowerExpr(email.Expr()), "ada@example.com")))
	if err != nil {
		fmt.Printf("failed to query user by email: %s\n", err)
		return
	}
	for _, user := range byEmail {
		fmt.Println(user.Nickname.Value)
	}

	// SQL: SELECT users.id, users.nickname FROM users ORDER BY users.id ASC
	names, err := rasql.All(ctx, executor, base.OrderBy(rasql.AscExpr(id.Expr())))
	if err != nil {
		fmt.Printf("failed to query user names: %s\n", err)
		return
	}
	for _, user := range names {
		if user.Nickname.Valid {
			fmt.Println(user.ID, user.Nickname.Value)
		} else {
			fmt.Println(user.ID, "NULL")
		}
	}

	// Output:
	// Ada
	// 1 Ada
	// 2 NULL
}
