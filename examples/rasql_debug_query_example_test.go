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

// debugQueryUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type debugQueryUsersDecoder struct{ result rasql.ResultSchema }

func (d debugQueryUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d debugQueryUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d debugQueryUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// debugQueryUsersColumns is every users column bound to one rasql.Source, so a
// caller can add a Where or OrderBy against the same columns debugQueryUsersQuery
// projects.
type debugQueryUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, *string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// debugQueryUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func debugQueryUsersQuery() (rasql.Query[store.UsersRow], debugQueryUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	var cols debugQueryUsersColumns
	if cols.ID, err = rasql.BindTypedColumn(users.ID()); err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindTypedColumn(users.Email()); err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullTypedColumn(users.Nickname()); err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindTypedColumn(users.Status()); err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindTypedColumn(users.FirstName()); err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindTypedColumn(users.LastName()); err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
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
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.ID().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.Email().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.Nickname().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.Status().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstName().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastName().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, debugQueryUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, debugQueryUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

// Example_rasql_debug_query renders a typed query's SQL with rasql.Render, which
// needs no database connection at all, and then runs the same query against a
// real database to show how many rows it returns.
func Example_rasql_debug_query() {
	base, cols, err := debugQueryUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}
	selected := base.Where(rasql.EqualValue(cols.ID.Expr(), int64(42)))

	// Render lowers the query to SQL text for a chosen dialect without
	// opening a database, which is exactly what inspecting a query before it
	// ever reaches a server needs.
	statement, err := rasql.Render(selected, dialect.PostgreSQL())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())

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

	// The table is empty, so a real database reports zero rows rather than
	// failing the way a fake one that answers every query with no columns at
	// all would.
	rows, err := rasql.All(ctx, executor, selected)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	fmt.Printf("%d result rows\n", len(rows))

	// Output:
	// SELECT "users"."id" AS "id", "users"."email" AS "email", "users"."nickname" AS "nickname", "users"."status" AS "status", "users"."first_name" AS "first_name", "users"."last_name" AS "last_name" FROM "users" WHERE ("users"."id" = $1)
	// [42]
	// 0 result rows
}
