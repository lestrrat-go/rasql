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

// namedScopeUsersDecoder decodes every column of the users table into a
// store.UsersRow, reusing the generated ScanRow method rather than restating
// the column order.
type namedScopeUsersDecoder struct{ result rasql.ResultSchema }

func (d namedScopeUsersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d namedScopeUsersDecoder) Presence() []rasql.Presence       { return nil }
func (d namedScopeUsersDecoder) DecodeRow(src rasql.ScanSource, row *store.UsersRow) error {
	return row.ScanRow(src)
}

// namedScopeUsersColumns is every users column bound to one rasql.Source, so a
// caller can add a Where or OrderBy against the same columns
// namedScopeUsersQuery projects.
type namedScopeUsersColumns struct {
	ID        rasql.Column[store.UsersRow, int64]
	Email     rasql.Column[store.UsersRow, string]
	Nickname  rasql.NullColumn[store.UsersRow, string]
	Status    rasql.Column[store.UsersRow, string]
	FirstName rasql.Column[store.UsersRow, string]
	LastName  rasql.Column[store.UsersRow, string]
}

// namedScopeUsersQuery builds the canonical Query[store.UsersRow] that projects
// every users column, in the order the generated row type scans them, and
// returns the bound columns so a caller can filter or order by them.
func namedScopeUsersQuery() (rasql.Query[store.UsersRow], namedScopeUsersColumns, error) {
	users := store.Users()
	def := store.UsersDef()
	source, err := rasql.SourceOf(users, "")
	if err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	var cols namedScopeUsersColumns
	if cols.ID, err = rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	if cols.Email, err = rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	if cols.Nickname, err = rasql.BindNullColumn[store.UsersRow, string](source, users.NicknameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	if cols.Status, err = rasql.BindColumn[store.UsersRow, string](source, users.StatusRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	if cols.FirstName, err = rasql.BindColumn[store.UsersRow, string](source, users.FirstNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	if cols.LastName, err = rasql.BindColumn[store.UsersRow, string](source, users.LastNameRef().Name(), ""); err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
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
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(users.IDRef().Name(), cols.ID.Expr(), def.Columns[0].Type, ""),
		rasql.Item(users.EmailRef().Name(), cols.Email.Expr(), def.Columns[1].Type, ""),
		rasql.NullItem(users.NicknameRef().Name(), cols.Nickname.NullExpr(), def.Columns[2].Type, ""),
		rasql.Item(users.StatusRef().Name(), cols.Status.Expr(), def.Columns[3].Type, ""),
		rasql.Item(users.FirstNameRef().Name(), cols.FirstName.Expr(), def.Columns[4].Type, ""),
		rasql.Item(users.LastNameRef().Name(), cols.LastName.Expr(), def.Columns[5].Type, ""),
	}, namedScopeUsersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.UsersRow]{}, namedScopeUsersColumns{}, err
	}
	return rasql.Select(source.Source(), projection), cols, nil
}

// namedScopeUsersScope is one named, reusable piece of a users query. Ruby's
// ActiveRecord calls this a scope and registers it on the model, as
// `scope :active, -> { where(status: "active") }`; rasql has no such registry,
// so a scope here is an ordinary Go value that the caller writes and names.
//
// Apply takes a query and returns a query, which is what lets two scopes chain
// in either order: rasql.Query is immutable, so every builder method returns a
// new value and the query handed to Apply is left as it was. Each scope carries
// the bound columns it reads, because a predicate is built from columns bound to
// one source rather than from a column name.
//
// A rasql.Scope is an unrelated thing: it is the transaction or savepoint
// callback rasql.Within runs.
type namedScopeUsersScope interface {
	Apply(rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow]
}

// namedScopeActive keeps the rows whose status column is "active".
type namedScopeActive struct{ cols namedScopeUsersColumns }

func (s namedScopeActive) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.Where(rasql.EqualValue(s.cols.Status.Expr(), "active"))
}

// namedScopeSurnamed keeps the rows whose last_name column equals the surname
// the caller gives, the way an ActiveRecord scope takes a lambda argument.
type namedScopeSurnamed struct {
	cols    namedScopeUsersColumns
	surname string
}

func (s namedScopeSurnamed) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.Where(rasql.EqualValue(s.cols.LastName.Expr(), s.surname))
}

// namedScopeByEmail sorts the rows by the email column, ascending. A scope adds
// an ordering as readily as a predicate, since both are builder methods on the
// same query.
type namedScopeByEmail struct{ cols namedScopeUsersColumns }

func (s namedScopeByEmail) Apply(q rasql.Query[store.UsersRow]) rasql.Query[store.UsersRow] {
	return q.OrderBy(rasql.AscExpr(s.cols.Email.Expr()))
}

// namedScopeApply applies each scope to the query in turn, left to right, which
// spells out what `Users.active.surnamed("Hopper").by_email` chains together in
// ActiveRecord. Two Where calls are combined with AND, so the order of the
// scopes changes nothing about the rows that come back.
func namedScopeApply(q rasql.Query[store.UsersRow], scopes ...namedScopeUsersScope) rasql.Query[store.UsersRow] {
	for _, scope := range scopes {
		q = scope.Apply(q)
	}
	return q
}

func Example_rasql_named_scope() {
	// This example names two filters and one ordering, then combines them two
	// ways against a single base query.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL, and
	// asks the server which engine profile it is.
	db, err := rasql.Open(ctx, database, dialect.SQLite())
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
		{ID: 1, Email: "ada@example.com", Status: "active", FirstName: "Ada", LastName: "Lovelace"},
		{ID: 2, Email: "bob@example.com", Status: "pending", FirstName: "Bob", LastName: "Hopper"},
		{ID: 3, Email: "cyd@example.com", Status: "active", FirstName: "Cyd", LastName: "Hopper"},
		{ID: 4, Email: "dee@example.com", Status: "active", FirstName: "Dee", LastName: "Hopper"},
	} {
		plan := store.NewUsersCreate().
			ID(user.ID).
			Email(user.Email).
			Status(user.Status).
			FirstName(user.FirstName).
			LastName(user.LastName).
			Plan()
		if _, err := rasql.ExecMutation(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	base, cols, err := namedScopeUsersQuery()
	if err != nil {
		fmt.Printf("failed to build users query: %s\n", err)
		return
	}
	active := namedScopeActive{cols: cols}
	byEmail := namedScopeByEmail{cols: cols}

	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE (users.status = ? AND users.last_name = ?) ORDER BY users.email (arguments: active, Hopper)
	hoppers, err := rasql.All(ctx, db,
		namedScopeApply(base, active, namedScopeSurnamed{cols: cols, surname: "Hopper"}, byEmail))
	if err != nil {
		fmt.Printf("failed to query active Hoppers: %s\n", err)
		return
	}
	for _, found := range hoppers {
		fmt.Printf("active Hopper: %s\n", found.Email)
	}

	// base still selects every row, because no scope changed it. Dropping the
	// surname scope reaches the fourth active user again.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE (users.status = ?) ORDER BY users.email (arguments: active)
	everyone, err := rasql.All(ctx, db, namedScopeApply(base, active, byEmail))
	if err != nil {
		fmt.Printf("failed to query active users: %s\n", err)
		return
	}
	for _, found := range everyone {
		fmt.Printf("active: %s\n", found.Email)
	}

	// Output:
	// active Hopper: cyd@example.com
	// active Hopper: dee@example.com
	// active: ada@example.com
	// active: cyd@example.com
	// active: dee@example.com
}
