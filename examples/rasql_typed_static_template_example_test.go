package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// rankedUser is a local result type rather than store.UsersRow, because the
// template's window function returns a rank column the users table does not
// have, and a generated row type has a field only for a real column.
type rankedUser struct {
	ID    int64
	Email string
	Rank  int64
}

// rankedUserDecoder decodes a rankedUser positionally, in the order the
// template below projects id, email, and rank.
type rankedUserDecoder struct{ result rasql.ResultSchema }

func (d rankedUserDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d rankedUserDecoder) Presence() []rasql.Presence       { return nil }
func (d rankedUserDecoder) DecodeRow(src rasql.ScanSource, row *rankedUser) error {
	return src.Scan(&row.ID, &row.Email, &row.Rank)
}

func Example_rasql_typed_static_template() {
	// A static template keeps complex SQL readable while rasql.Native maps its
	// result into a normal Go type through the same typed decode path every
	// other query uses.
	ctx := context.Background()
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
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []store.UsersRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		plan := store.NewUsersCreate().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	parsed, err := namedsql.Parse("ranked_users", `WITH ranked_users AS (
		SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank
		FROM users
	)
	SELECT id, email, rank FROM ranked_users WHERE id >= {{bind "minimum_id"}} ORDER BY rank`)
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	bound, err := compiled.Bind(map[string]any{"minimum_id": 2})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}
	nativeArgs := make([]rasql.NativeArgument, len(bound.Args()))
	for i, arg := range bound.Args() {
		nativeArgs[i] = rasql.NativeArgument{Value: arg}
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NativeProjection[rankedUser](rankedUserDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}
	q, err := rasql.Native(rasql.NativeStatement{
		Engine: "sqlite",
		SQL:    bound.SQL(),
		Args:   nativeArgs,
	}, projection, rasql.Many)
	if err != nil {
		fmt.Printf("failed to build native query: %s\n", err)
		return
	}

	// SQL: WITH ranked_users AS (SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank FROM users) SELECT id, email, rank FROM ranked_users WHERE id >= ? ORDER BY rank (argument: 2)
	rows, err := rasql.All(ctx, executor, q)
	if err != nil {
		fmt.Printf("failed to query ranked users: %s\n", err)
		return
	}
	for _, user := range rows {
		fmt.Println(user.Rank, user.Email)
	}

	// Output:
	// 2 bob@example.com
	// 3 cyd@example.com
}
