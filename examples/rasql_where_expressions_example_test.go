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

// whereExprUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type whereExprUsersDecoder struct{ result rasql.ResultSchema }

func (d whereExprUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d whereExprUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d whereExprUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// whereExprUsersColumns is every users column bound to one rasql.Source, so a
// caller can add a Where or OrderBy against the same columns whereExprUsersQuery
// projects.
type whereExprUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// whereExprUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func whereExprUsersQuery() (rasql.Query[store.UsersRow], whereExprUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	var cols whereExprUsersColumns
	if cols.ID, err = rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullColumn[store.UsersRow, string](source, users.NicknameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindColumn[store.UsersRow, string](source, users.StatusRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindColumn[store.UsersRow, string](source, users.FirstNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindColumn[store.UsersRow, string](source, users.LastNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: users.IDRef().Name(), Type: def.Columns[0].Type},
		rasql.ResultColumn{Name: users.EmailRef().Name(), Type: def.Columns[1].Type},
		rasql.ResultColumn{Name: users.NicknameRef().Name(), Type: def.Columns[2].Type, Nullable: true},
		rasql.ResultColumn{Name: users.StatusRef().Name(), Type: def.Columns[3].Type},
		rasql.ResultColumn{Name: users.FirstNameRef().Name(), Type: def.Columns[4].Type},
		rasql.ResultColumn{Name: users.LastNameRef().Name(), Type: def.Columns[5].Type},
	)
	if err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.IDRef().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.EmailRef().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.NicknameRef().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.StatusRef().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstNameRef().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastNameRef().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, whereExprUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, whereExprUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

// Example_rasql_where_expressions combines a comparison and a null check with
// And, which is what a predicate richer than one comparison needs. Every
// plain value still travels as a bound argument: Value binds it automatically,
// and the renderer turns it into the dialect's placeholder.
func Example_rasql_where_expressions() {
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
	grace, ed := "Grace", "Ed"
	for _, user := range []store.UsersRow{
		{ID: 5, Email: "ada@example.com"},
		{ID: 15, Email: "grace@example.com", Nickname: &grace},
		{ID: 17, Email: "nia@example.com"},
		{ID: 20, Email: "edsger@example.com", Nickname: &ed},
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

	base, cols, err := whereExprUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}

	// id=17 has id > 10 but no nickname, so it shows the And's second
	// condition is doing real work rather than repeating the first.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE (users.id > ? AND users.nickname IS NOT NULL) ORDER BY users.id DESC (argument: 10)
	rows, err := rasql.All(ctx, executor,
		base.Where(rasql.And(
			rasql.GreaterValue(cols.ID.Expr(), int64(10)),
			rasql.IsNotNull(cols.Nickname.NullExpr()),
		)).OrderBy(rasql.DescExpr(cols.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range rows {
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// 20 edsger@example.com
	// 15 grace@example.com
}
