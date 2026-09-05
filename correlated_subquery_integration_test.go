//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// correlatedUser and correlatedOrder are the rows the correlated tests write and
// read back. The users carry one row with two orders, one with none, and one
// with a single order, so an EXISTS, a NOT EXISTS and a per-row count each
// return a different set.
type correlatedUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

type correlatedOrder struct {
	ID     int64 `rasql:"id"`
	UserID int64 `rasql:"user_id"`
	Amount int64 `rasql:"amount"`
}

// correlatedUserOrders decodes a user beside the count a correlated scalar
// subquery computed for that user's own row.
type correlatedUserOrders struct {
	ID     int64 `rasql:"id"`
	Orders int64 `rasql:"orders"`
}

// TestCorrelatedSubqueryAgainstLiveDatabases runs a correlated EXISTS, a
// correlated NOT EXISTS and a correlated scalar subquery against every engine
// rasql supports, and requires each to return the rows the correlation asks
// for rather than merely to run. A subquery that ignored the enclosing row
// would return every user for the EXISTS and none for the NOT EXISTS, so the
// row sets are what prove the correlation reached the server intact.
//
// PostgreSQL and MySQL skip when their DSN is unset; SQLite always runs, in
// memory.
func TestCorrelatedSubqueryAgainstLiveDatabases(t *testing.T) {
	for _, engine := range []correlatedEngine{
		{
			name:    "postgresql",
			open:    dbtest.PostgreSQLDB,
			dialect: dialect.PostgreSQL(),
			// PostgreSQL is the one engine that refuses a bound Go integer
			// projected on its own inside EXISTS; the subtest that sets this
			// field carries the measured reason.
			refusesBoundIntegerBody: true,
		},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
		{name: "sqlite", open: openCorrelatedSQLite, dialect: dialect.SQLite()},
	} {
		t.Run(engine.name, func(t *testing.T) {
			testCorrelatedSubquery(t, engine)
		})
	}
}

// correlatedEngine describes one live server the correlated shapes are proved
// against.
type correlatedEngine struct {
	name    string
	open    func(*testing.T) *sql.DB
	dialect dialect.Dialect
	// refusesBoundIntegerBody records whether the engine rejects
	// Project(Bind(1)) as the body of an EXISTS. It is the one point the three
	// servers disagree on, so the subtest asserting it carries the reason.
	refusesBoundIntegerBody bool
}

func testCorrelatedSubquery(t *testing.T, engine correlatedEngine) {
	t.Helper()

	database := engine.open(t)
	db, err := rasql.New(database, engine.dialect)
	require.NoError(t, err)

	users, orders := createCorrelatedFixture(t, db)
	usersRef := users.Ref()
	usersID := usersRef.Column("id")
	ordersRef := orders.Ref()
	ordersID := ordersRef.Column("id")
	ordersUserID := ordersRef.Column("user_id")

	// SELECT orders.id FROM orders WHERE orders.user_id = users.id, correlated
	// with the enclosing users statement. EXISTS reads no value, so what the
	// body projects is arbitrary; a column of the subquery's own table is the
	// portable choice, for the reason the bound-value subtest below measures.
	hasOrder, err := query.NewSelect(ordersRef, ordersID)
	require.NoError(t, err)
	hasOrder, err = hasOrder.WithCorrelation(usersRef)
	require.NoError(t, err)
	hasOrder, err = hasOrder.WithWhere(query.Equal(ordersUserID, usersID))
	require.NoError(t, err)

	t.Run("exists keeps the users that have an order", func(t *testing.T) {
		got, err := rasql.SelectFrom(users).
			Where(query.Exists(hasOrder)).
			OrderAsc(usersID).
			All(t.Context(), db)
		require.NoError(t, err)
		require.Equal(t, []correlatedUser{
			{ID: 1, Email: "ada@example.com"},
			{ID: 3, Email: "cyd@example.com"},
		}, got)
	})

	t.Run("not exists keeps the user that has none", func(t *testing.T) {
		got, err := rasql.SelectFrom(users).
			Where(query.NotExists(hasOrder)).
			OrderAsc(usersID).
			All(t.Context(), db)
		require.NoError(t, err)
		require.Equal(t, []correlatedUser{{ID: 2, Email: "bob@example.com"}}, got)
	})

	t.Run("a scalar subquery counts each user's own orders", func(t *testing.T) {
		// The count differs per row, so a subquery evaluated once for the
		// whole statement would report 3 for every user instead of 2, 0 and 1.
		counted, err := query.NewSelect(ordersRef, query.Project(query.CountAll()))
		require.NoError(t, err)
		counted, err = counted.WithCorrelation(usersRef)
		require.NoError(t, err)
		counted, err = counted.WithWhere(query.Equal(ordersUserID, usersID))
		require.NoError(t, err)

		got, err := rasql.DecodeFrom[correlatedUserOrders](users).
			Project(usersID, query.Project(query.Scalar(counted)).As("orders")).
			OrderAsc(usersID).
			All(t.Context(), db)
		require.NoError(t, err)
		require.Equal(t, []correlatedUserOrders{
			{ID: 1, Orders: 2},
			{ID: 2, Orders: 0},
			{ID: 3, Orders: 1},
		}, got)
	})

	t.Run("a bound value as the exists body is not portable", func(t *testing.T) {
		// SELECT 1 is the conventional EXISTS body in hand-written SQL, and
		// Project(Bind(1)) is how it is written here -- but a bound value is a
		// placeholder, not the literal 1. PostgreSQL has nothing to infer that
		// placeholder's type from in a projection standing on its own, so it
		// types it as text, and pgx then refuses to encode a Go int as text.
		// MySQL and SQLite both run it. Binding a string instead runs
		// everywhere, which is what shows PostgreSQL typed the parameter
		// rather than refusing a projected parameter outright.
		bound, err := query.NewSelect(ordersRef, query.Project(query.Bind(1)))
		require.NoError(t, err)
		bound, err = bound.WithCorrelation(usersRef)
		require.NoError(t, err)
		bound, err = bound.WithWhere(query.Equal(ordersUserID, usersID))
		require.NoError(t, err)

		got, err := rasql.SelectFrom(users).
			Where(query.Exists(bound)).
			OrderAsc(usersID).
			All(t.Context(), db)
		if engine.refusesBoundIntegerBody {
			require.Error(t, err,
				"a bound Go integer projected on its own inside EXISTS must fail on this engine, or the portable-body advice has nothing behind it")
		} else {
			require.NoError(t, err)
			require.Equal(t, []correlatedUser{
				{ID: 1, Email: "ada@example.com"},
				{ID: 3, Email: "cyd@example.com"},
			}, got)
		}

		text, err := query.NewSelect(ordersRef, query.Project(query.Bind("1")))
		require.NoError(t, err)
		text, err = text.WithCorrelation(usersRef)
		require.NoError(t, err)
		text, err = text.WithWhere(query.Equal(ordersUserID, usersID))
		require.NoError(t, err)

		got, err = rasql.SelectFrom(users).
			Where(query.Exists(text)).
			OrderAsc(usersID).
			All(t.Context(), db)
		require.NoError(t, err,
			"a bound string projected on its own runs on every engine, including the one that refuses the integer")
		require.Equal(t, []correlatedUser{
			{ID: 1, Email: "ada@example.com"},
			{ID: 3, Email: "cyd@example.com"},
		}, got)
	})

	t.Run("exists accepts a limit on every engine", func(t *testing.T) {
		// render.Select gates a LIMIT inside IN (SELECT …) behind
		// dialect.CapabilitySubqueryLimit because MySQL refuses that shape.
		// EXISTS is not one of the shapes MySQL's error 1235 names, so no
		// capability gates it here; running it is what says so.
		limited, err := hasOrder.WithLimit(1)
		require.NoError(t, err)
		got, err := rasql.SelectFrom(users).
			Where(query.Exists(limited)).
			OrderAsc(usersID).
			All(t.Context(), db)
		require.NoError(t, err)
		require.Equal(t, []correlatedUser{
			{ID: 1, Email: "ada@example.com"},
			{ID: 3, Email: "cyd@example.com"},
		}, got)
	})
}

// TestMySQLRefusesALimitInsideInSelectButNotInsideExists pins the difference
// the previous test's last subtest rests on, against MySQL itself. rasql
// refuses to render a LIMIT inside IN (SELECT …) for MySQL, so the refused
// shape is sent as SQL text here, exactly as TestIndexedTextRequiresWidthOnMySQL
// sends the statement its own render-time check pre-empts. The EXISTS half goes
// through the server too, so "MySQL allows it there" is measured rather than
// assumed.
func TestMySQLRefusesALimitInsideInSelectButNotInsideExists(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := rasql.New(database, dialect.MySQL())
	require.NoError(t, err)

	users, _ := createCorrelatedFixture(t, db)
	name := users.Ref().Name()

	_, err = database.QueryContext(t.Context(),
		"SELECT `id` FROM `"+name+"` WHERE `id` IN (SELECT `id` FROM `"+name+"` LIMIT 1)")
	require.Error(t, err,
		"MySQL itself refuses a LIMIT inside IN (SELECT …), which is why rasql refuses to render one")
	// The code is read off the driver's parsed error rather than matched
	// against the message text, for the reason internal/dbtest/mysql.go gives.
	var mysqlErr *mysql.MySQLError
	require.ErrorAs(t, err, &mysqlErr,
		"the refusal must come from MySQL itself, not from a connection or driver failure")
	require.EqualValues(t, 1235, mysqlErr.Number,
		"the refusal is MySQL error 1235, the one dialect.CapabilitySubqueryLimit exists for")

	rows, err := database.QueryContext(t.Context(),
		"SELECT `id` FROM `"+name+"` WHERE EXISTS (SELECT 1 FROM `"+name+"` LIMIT 1)")
	require.NoError(t, err,
		"MySQL accepts the same LIMIT inside EXISTS, so no capability gates it there")
	require.NoError(t, rows.Close())
	require.NoError(t, rows.Err())
}

// createCorrelatedFixture creates the two tables under per-run unique names and
// fills them with the rows every correlated test reads. The names come from
// dbtest.UniqueName so a live run can only ever drop tables it created itself.
func createCorrelatedFixture(t *testing.T, db rasql.DB) (rasql.Table[correlatedUser], rasql.Table[correlatedOrder]) {
	t.Helper()

	users, err := rasql.TableOf[correlatedUser](schema.TableDef{
		Name: dbtest.UniqueName(t, "rasql_correlated_users"),
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(191)}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := rasql.TableOf[correlatedOrder](schema.TableDef{
		Name: dbtest.UniqueName(t, "rasql_correlated_orders"),
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	require.NoError(t, rasql.CreateTable(t.Context(), db, orders))

	for _, user := range []correlatedUser{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		_, err := rasql.Insert(t.Context(), db, users, user)
		require.NoError(t, err)
	}
	for _, order := range []correlatedOrder{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 1, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		_, err := rasql.Insert(t.Context(), db, orders, order)
		require.NoError(t, err)
	}
	return users, orders
}

// openCorrelatedSQLite returns an in-memory SQLite database on a single
// connection, since an in-memory database belongs to the connection that opened
// it.
func openCorrelatedSQLite(t *testing.T) *sql.DB {
	t.Helper()

	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)
	return database
}
