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

// queryErrorsUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type queryErrorsUsersDecoder struct{ result rasql.ResultSchema }

func (d queryErrorsUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d queryErrorsUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d queryErrorsUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// queryErrorsUsersColumns is every users column bound to one rasql.Source, so a
// caller can add a Where or OrderBy against the same columns queryErrorsUsersQuery
// projects.
type queryErrorsUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// queryErrorsUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func queryErrorsUsersQuery() (rasql.Query[store.UsersRow], queryErrorsUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	var cols queryErrorsUsersColumns
	if cols.ID, err = rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullColumn[store.UsersRow, string](source, users.NicknameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindColumn[store.UsersRow, string](source, users.StatusRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindColumn[store.UsersRow, string](source, users.FirstNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindColumn[store.UsersRow, string](source, users.LastNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
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
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.IDRef().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.EmailRef().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.NicknameRef().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.StatusRef().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstNameRef().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastNameRef().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, queryErrorsUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, queryErrorsUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

// Example_rasql_query_errors shows where a failing query reports itself: the
// statement's own problems arrive as the error Rows returns, and everything
// that goes wrong once rows are moving arrives inside the loop.
func Example_rasql_query_errors() {
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
	plan := store.NewUsersCreate().ID(1).Email("ada@example.com").FirstName("First").LastName("Last").Plan()
	if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	base, _, err := queryErrorsUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}

	rows, err := rasql.Rows(ctx, executor, base)
	if err != nil {
		// The statement could not be validated or rendered.
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for user, err := range rows {
		if err != nil {
			// Execution or scanning failed. No further rows follow.
			fmt.Printf("failed to read user: %s\n", err)
			return
		}
		fmt.Println(user.Email)
	}

	// Dropping the table shows which of the two checks catches an execution
	// failure. The statement still validates and renders, so Rows returns no
	// error and the database's complaint arrives on the first step of the loop.
	if _, err := database.ExecContext(ctx, "DROP TABLE users"); err != nil {
		fmt.Printf("failed to drop users table: %s\n", err)
		return
	}
	dropped, err := rasql.Rows(ctx, executor, base)
	fmt.Println("error from Rows:", err)
	for _, err := range dropped {
		fmt.Println("error from the loop:", err)
	}

	// Output:
	// ada@example.com
	// error from Rows: <nil>
	// error from the loop: rasql: execute query: SQL logic error: no such table: users (1)
}
