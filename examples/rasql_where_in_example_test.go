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

// whereInUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type whereInUsersDecoder struct{ result rasql.ResultSchema }

func (d whereInUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d whereInUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d whereInUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// whereInUsersColumns is every users column bound to one rasql.Source, so a
// caller can add a Where or OrderBy against the same columns whereInUsersQuery
// projects.
type whereInUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// whereInUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func whereInUsersQuery() (rasql.Query[store.UsersRow], whereInUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	var cols whereInUsersColumns
	if cols.ID, err = rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullColumn[store.UsersRow, string](source, users.NicknameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindColumn[store.UsersRow, string](source, users.StatusRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindColumn[store.UsersRow, string](source, users.FirstNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindColumn[store.UsersRow, string](source, users.LastNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
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
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.IDRef().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.EmailRef().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.NicknameRef().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.StatusRef().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstNameRef().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastNameRef().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, whereInUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, whereInUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

func Example_rasql_where_in() {
	// This example selects rows whose id is one of a fixed set of values.
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
	// Create the table described by the generated users descriptor.
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

	base, cols, err := whereInUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}

	// InValues binds one placeholder per value and skips the users whose id is
	// not in the list. It takes the first value separately so an empty IN
	// list, which is not legal SQL, cannot be written at all.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE users.id IN (?, ?) ORDER BY users.id ASC (arguments: 1, 3)
	rows, err := rasql.All(ctx, executor,
		base.Where(rasql.InValues(cols.ID.Expr(), int64(1), int64(3))).
			OrderBy(rasql.AscExpr(cols.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, found := range rows {
		fmt.Println(found.Email)
	}

	// Output:
	// ada@example.com
	// cyd@example.com
}
