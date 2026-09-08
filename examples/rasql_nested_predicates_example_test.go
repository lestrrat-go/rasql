package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// nestedPredUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type nestedPredUsersDecoder struct{ result rasql.ResultSchema }

func (d nestedPredUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d nestedPredUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d nestedPredUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// nestedPredUsersColumns is every users column bound to one rasql.Source, so a
// caller can add a Where or OrderBy against the same columns nestedPredUsersQuery
// projects.
type nestedPredUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, *string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// nestedPredUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func nestedPredUsersQuery() (rasql.Query[store.UsersRow], nestedPredUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	var cols nestedPredUsersColumns
	if cols.ID, err = rasql.BindTypedColumn(users.ID()); err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindTypedColumn(users.Email()); err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullTypedColumn(users.Nickname()); err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindTypedColumn(users.Status()); err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindTypedColumn(users.FirstName()); err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindTypedColumn(users.LastName()); err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: users.ID().Name(), Type: def.Columns[0].Type},
		rasql.ResultColumn{Name: users.Email().Name(), Type: def.Columns[1].Type},
		rasql.ResultColumn{Name: users.Nickname().Name(), Type: def.Columns[2].Type, Nullable: true},
		rasql.ResultColumn{Name: users.Status().Name(), Type: def.Columns[3].Type},
		rasql.ResultColumn{Name: users.FirstName().Name(), Type: def.Columns[4].Type},
		rasql.ResultColumn{Name: users.LastName().Name(), Type: def.Columns[5].Type},
	)
	if err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.ID().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.Email().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.Nickname().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.Status().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstName().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastName().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, nestedPredUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, nestedPredUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

// Example_rasql_nested_predicates builds a predicate tree several levels deep
// and shows the SQL it renders, which is what a filter that mixes AND and OR
// needs. rasql.And and rasql.Or take predicates and return one, so either
// holds the other to any depth.
func Example_rasql_nested_predicates() {
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
	alan := "Alan"
	for _, user := range []store.UsersRow{
		{ID: 5, Email: "ada@example.com"},
		{ID: 7, Email: "linus@other.org"},
		{ID: 15, Email: "grace@example.com"},
		{ID: 25, Email: "alan@example.com", Nickname: &alan},
		{ID: 30, Email: "extra@example.com"},
	} {
		plan := store.NewUsersCreate().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last")
		if user.Nickname != nil {
			plan = plan.Nickname(user.Nickname)
		}
		if _, err := rasql.ExecMutation(ctx, executor, plan.Plan()); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	base, cols, err := nestedPredUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}

	// The inner And sits inside an Or, which sits inside the outer And this
	// Where call adds, and the whole tree is one predicate. Row 30 has id > 20
	// but no nickname, so it shows the innermost And is not vacuous.
	selected := base.Where(rasql.And(
		rasql.LikeValue(cols.Email.Expr(), "%@example.com"),
		rasql.Or(
			rasql.LessValue(cols.ID.Expr(), int64(10)),
			rasql.And(
				rasql.GreaterValue(cols.ID.Expr(), int64(20)),
				rasql.IsNotNull(cols.Nickname.NullExpr()),
			),
		),
	)).OrderBy(rasql.AscExpr(cols.ID.Expr()))

	// Every level of the tree renders its own parentheses, so the SQL groups
	// the way the Go code nests rather than by the database's operator
	// precedence. Render proves it, rather than a comment merely claiming it.
	statement, err := rasql.Render(selected, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	found, err := rasql.All(ctx, executor, selected)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range found {
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// SELECT "users"."id" AS "id", "users"."email" AS "email", "users"."nickname" AS "nickname", "users"."status" AS "status", "users"."first_name" AS "first_name", "users"."last_name" AS "last_name" FROM "users" WHERE (("users"."email" LIKE ?) AND (("users"."id" < ?) OR (("users"."id" > ?) AND ("users"."nickname" IS NOT NULL)))) ORDER BY "users"."id"
	// 5 ada@example.com
	// 25 alan@example.com
}
