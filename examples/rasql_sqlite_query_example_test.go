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

// sqliteQueryUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type sqliteQueryUsersDecoder struct{ result rasql.ResultSchema }

func (d sqliteQueryUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d sqliteQueryUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d sqliteQueryUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// sqliteQueryUsersColumns is every users column bound to one rasql.Source, so a caller
// can add a Where or OrderBy against the same columns sqliteQueryUsersQuery projects.
type sqliteQueryUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// sqliteQueryUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func sqliteQueryUsersQuery() (rasql.Query[store.UsersRow], sqliteQueryUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	var cols sqliteQueryUsersColumns
	if cols.ID, err = rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullColumn[store.UsersRow, string](source, users.NicknameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindColumn[store.UsersRow, string](source, users.StatusRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindColumn[store.UsersRow, string](source, users.FirstNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindColumn[store.UsersRow, string](source, users.LastNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
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
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.IDRef().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.EmailRef().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.NicknameRef().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.StatusRef().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstNameRef().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastNameRef().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, sqliteQueryUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, sqliteQueryUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

func Example_rasql_sqlite_query() {
	// This example creates, inserts, and reads one generated row with SQLite.
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

	// store.Users() returns the generated table value, which carries the row
	// type and one accessor method per column.
	users := store.Users()

	// Create the schema described by the generated table descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// The generated create builder binds the row's fields as values, through
	// the column accessors the generator wrote.
	plan := store.NewUsersCreate().ID(42).Email("ada@example.com").FirstName("First").LastName("Last").Plan()
	if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The query binds one column per users field, so One returns a decoded
	// store.UsersRow.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE users.id = ? (argument: 42)
	base, cols, err := sqliteQueryUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}
	user, err := rasql.One(ctx, executor, base.Where(rasql.EqualValue(cols.ID.Expr(), int64(42))))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}

	fmt.Println(user.Email)

	// Output:
	// ada@example.com
}
