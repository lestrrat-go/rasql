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
	// OrderCount is written by the UPDATE whose SET value is a correlated
	// scalar subquery, and is zero everywhere else.
	OrderCount int64 `rasql:"order_count"`
}

type correlatedOrder struct {
	ID     int64 `rasql:"id"`
	UserID int64 `rasql:"user_id"`
	Amount int64 `rasql:"amount"`
}

type correlatedUserDecoder struct{ schema rasql.ResultSchema }

func (d correlatedUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (correlatedUserDecoder) Presence() []rasql.Presence         { return nil }
func (d correlatedUserDecoder) DecodeRow(source rasql.ScanSource, row *correlatedUser) error {
	return source.Scan(&row.ID, &row.Email, &row.OrderCount)
}

// correlatedUserColumns binds users' typed columns through the same relation,
// so the columns used to seed and filter agree with the columns the
// projection below reads.
func correlatedUserColumns(t *testing.T, users rasql.Table[correlatedUser]) (
	rasql.TypedRelation[correlatedUser], rasql.Column[correlatedUser, int64], rasql.Column[correlatedUser, string], rasql.Column[correlatedUser, int64],
) {
	t.Helper()

	relation, err := rasql.SourceOf(users, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[correlatedUser, int64](relation, "id", "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[correlatedUser, string](relation, "email", "")
	require.NoError(t, err)
	orderCount, err := rasql.BindColumn[correlatedUser, int64](relation, "order_count", "")
	require.NoError(t, err)
	return relation, id, email, orderCount
}

func correlatedOrderColumns(t *testing.T, orders rasql.Table[correlatedOrder]) (
	rasql.TypedRelation[correlatedOrder], rasql.Column[correlatedOrder, int64], rasql.Column[correlatedOrder, int64], rasql.Column[correlatedOrder, int64],
) {
	t.Helper()

	relation, err := rasql.SourceOf(orders, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[correlatedOrder, int64](relation, "id", "")
	require.NoError(t, err)
	userID, err := rasql.BindColumn[correlatedOrder, int64](relation, "user_id", "")
	require.NoError(t, err)
	amount, err := rasql.BindColumn[correlatedOrder, int64](relation, "amount", "")
	require.NoError(t, err)
	return relation, id, userID, amount
}

func correlatedUserProjection(
	t *testing.T, id rasql.Column[correlatedUser, int64], email rasql.Column[correlatedUser, string], orderCount rasql.Column[correlatedUser, int64],
) rasql.Projection[correlatedUser] {
	t.Helper()

	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(191)}},
		rasql.ResultColumn{Name: "order_count", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", email.Expr(), schema.TextType{Width: schema.NewTextWidth(191)}, ""),
		rasql.Item("order_count", orderCount.Expr(), schema.IntegerType{}, ""),
	}, correlatedUserDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

// correlatedExecutor builds the executor engine.dialect needs, reading the
// engine straight from db's own dialect rather than from a separate
// parameter, so a helper that only has db still resolves the right profile.
func correlatedExecutor(t *testing.T, db rasql.DB) rasql.Executor {
	t.Helper()

	var profile rasql.EngineProfile
	var err error
	switch db.Dialect().Name() {
	case "postgresql":
		profile, err = rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	case "mysql":
		profile, err = rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	default:
		profile, err = rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	}
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	return executor
}

// correlatedAllUsers reads every user back in id order, through the typed
// Query API, the way every subtest below confirms what a write actually did.
func correlatedAllUsers(
	t *testing.T, executor rasql.Executor, relation rasql.TypedRelation[correlatedUser],
	projection rasql.Projection[correlatedUser], id rasql.Column[correlatedUser, int64],
) []correlatedUser {
	t.Helper()

	rows, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), projection).OrderBy(rasql.AscExpr(id.Expr())))
	require.NoError(t, err)
	return rows
}

// correlatedExecStatement adapts a validated query.WriteStatement built
// directly against the query package, as every write subtest below does for
// its correlated EXISTS or scalar-subquery clause, to the typed executor.
func correlatedExecStatement(t *testing.T, executor rasql.Executor, statement query.WriteStatement) {
	t.Helper()

	plan, err := rasql.NewStatementPlan(statement)
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
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
	executor := correlatedExecutor(t, db)

	users, orders := createCorrelatedFixture(t, db)
	usersRelation, usersID, usersEmail, usersOrderCount := correlatedUserColumns(t, users)
	ordersRelation, ordersID, ordersUserID, _ := correlatedOrderColumns(t, orders)
	userProjection := correlatedUserProjection(t, usersID, usersEmail, usersOrderCount)

	// SELECT orders.id FROM orders WHERE orders.user_id = users.id, correlated
	// with the enclosing users statement. EXISTS reads no value, so what the
	// body projects is arbitrary; a column of the subquery's own table is the
	// portable choice, for the reason the bound-value subtest below measures.
	ordersIDProjection, err := rasql.Scalar("id", ordersID.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	hasOrder := rasql.Select(ordersRelation.Source(), ordersIDProjection).
		Correlated(usersRelation.Source()).
		Where(rasql.EqualExpr(ordersUserID.Expr(), usersID.Expr()))

	t.Run("exists keeps the users that have an order", func(t *testing.T) {
		exists, err := rasql.ExistsQuery(hasOrder)
		require.NoError(t, err)
		got, err := rasql.All(t.Context(), executor, rasql.Select(usersRelation.Source(), userProjection).
			Where(exists).
			OrderBy(rasql.AscExpr(usersID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []correlatedUser{
			{ID: 1, Email: "ada@example.com"},
			{ID: 3, Email: "cyd@example.com"},
		}, got)
	})

	t.Run("not exists keeps the user that has none", func(t *testing.T) {
		notExists, err := rasql.NotExistsQuery(hasOrder)
		require.NoError(t, err)
		got, err := rasql.All(t.Context(), executor, rasql.Select(usersRelation.Source(), userProjection).
			Where(notExists).
			OrderBy(rasql.AscExpr(usersID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []correlatedUser{{ID: 2, Email: "bob@example.com"}}, got)
	})

	t.Run("a scalar subquery counts each user's own orders", func(t *testing.T) {
		// The canonical typed API has no way to express this. ProjectionItem
		// only wraps Expr[T]/NullExpr[T], and every constructor that builds one
		// (BindColumn, Value, the typed operators) starts from a column or a
		// bound Go value; none of them lifts an arbitrary query.Expression such
		// as query.Scalar(counted), a correlated scalar subquery, into Expr[T].
		// ExistsQuery, NotExistsQuery, InQuery and NotInQuery cover a subquery
		// standing in a boolean predicate position; nothing in query_api.go,
		// expression.go or expression_operators.go covers one standing in a
		// SELECT list's value position. The equivalent case in an UPDATE's SET
		// assignment, further down in TestCorrelatedWriteAgainstLiveDatabases,
		// stays covered: it builds the statement directly against the query
		// package and never needs Expr[T] at all.
		t.Skip("no typed equivalent of a projected scalar subquery (query.Scalar as a SELECT-list value); see the comment above")
	})

	t.Run("a bound value as the exists body is not portable", func(t *testing.T) {
		// SELECT 1 is the conventional EXISTS body in hand-written SQL, and
		// Value(1) is how it is written here -- but a bound value is a
		// placeholder, not the literal 1. PostgreSQL has nothing to infer that
		// placeholder's type from in a projection standing on its own, so it
		// types it as text, and pgx then refuses to encode a Go int as text.
		// MySQL and SQLite both run it. Binding a string instead runs
		// everywhere, which is what shows PostgreSQL typed the parameter
		// rather than refusing a projected parameter outright.
		boundBody, err := rasql.Scalar("value", rasql.Value(1), schema.IntegerType{}, "")
		require.NoError(t, err)
		bound := rasql.Select(ordersRelation.Source(), boundBody).
			Correlated(usersRelation.Source()).
			Where(rasql.EqualExpr(ordersUserID.Expr(), usersID.Expr()))
		boundExists, err := rasql.ExistsQuery(bound)
		require.NoError(t, err)

		got, err := rasql.All(t.Context(), executor, rasql.Select(usersRelation.Source(), userProjection).
			Where(boundExists).
			OrderBy(rasql.AscExpr(usersID.Expr())))
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

		textBody, err := rasql.Scalar("value", rasql.Value("1"), schema.TextType{}, "")
		require.NoError(t, err)
		text := rasql.Select(ordersRelation.Source(), textBody).
			Correlated(usersRelation.Source()).
			Where(rasql.EqualExpr(ordersUserID.Expr(), usersID.Expr()))
		textExists, err := rasql.ExistsQuery(text)
		require.NoError(t, err)

		got, err = rasql.All(t.Context(), executor, rasql.Select(usersRelation.Source(), userProjection).
			Where(textExists).
			OrderBy(rasql.AscExpr(usersID.Expr())))
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
		limited, err := hasOrder.Limit(1)
		require.NoError(t, err)
		limitedExists, err := rasql.ExistsQuery(limited)
		require.NoError(t, err)
		got, err := rasql.All(t.Context(), executor, rasql.Select(usersRelation.Source(), userProjection).
			Where(limitedExists).
			OrderBy(rasql.AscExpr(usersID.Expr())))
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
			{Name: "order_count", Type: schema.IntegerType{}},
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

	executor := correlatedExecutor(t, db)

	_, usersID, usersEmail, usersOrderCount := correlatedUserColumns(t, users)
	for _, user := range []correlatedUser{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		plan, err := rasql.NewCreatePlan(users,
			rasql.SetField(usersID, user.ID),
			rasql.SetField(usersEmail, user.Email),
			rasql.SetField(usersOrderCount, user.OrderCount),
		)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}
	_, ordersID, ordersUserID, ordersAmount := correlatedOrderColumns(t, orders)
	for _, order := range []correlatedOrder{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 1, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		plan, err := rasql.NewCreatePlan(orders,
			rasql.SetField(ordersID, order.ID),
			rasql.SetField(ordersUserID, order.UserID),
			rasql.SetField(ordersAmount, order.Amount),
		)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
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

// TestCorrelatedWriteAgainstLiveDatabases runs a correlated subquery in each of
// the three write clauses that admit one -- a DELETE's WHERE, an UPDATE's WHERE,
// and an UPDATE's SET assignment value -- against every engine rasql supports,
// and requires each to change exactly the rows the correlation names. The row a
// correlated subquery reads in a write statement is the row being written, so a
// subquery evaluated once for the whole statement would touch every row or none
// and fail each of these.
//
// The subqueries here read a table other than the write target. A correlated
// subquery whose own FROM is the target is the shape MySQL answers 1093 to, and
// TestCorrelatedWriteSubqueryFollowsTheSameCapability in the render package
// covers that boundary.
//
// PostgreSQL and MySQL skip when their DSN is unset; SQLite always runs, in
// memory.
func TestCorrelatedWriteAgainstLiveDatabases(t *testing.T) {
	for _, engine := range []correlatedEngine{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
		{name: "sqlite", open: openCorrelatedSQLite, dialect: dialect.SQLite()},
	} {
		t.Run(engine.name, func(t *testing.T) {
			testCorrelatedWrite(t, engine)
		})
	}
}

func testCorrelatedWrite(t *testing.T, engine correlatedEngine) {
	t.Helper()

	database := engine.open(t)
	db, err := rasql.New(database, engine.dialect)
	require.NoError(t, err)
	executor := correlatedExecutor(t, db)

	// Each clause gets its own pair of tables, since each one writes. The names
	// come from dbtest.UniqueName, so the three pairs never collide even on the
	// one SQLite connection they share.
	t.Run("a DELETE's WHERE clause", func(t *testing.T) {
		users, orders := createCorrelatedFixture(t, db)
		usersRelation, usersID, usersEmail, usersOrderCount := correlatedUserColumns(t, users)
		userProjection := correlatedUserProjection(t, usersID, usersEmail, usersOrderCount)

		statement, err := query.NewDelete(users.Ref())
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.Exists(correlatedOrdersOfUser(t, users, orders, orders.Ref().Column("id"))))
		require.NoError(t, err)
		correlatedExecStatement(t, executor, statement)

		remaining := correlatedAllUsers(t, executor, usersRelation, userProjection, usersID)
		require.Equal(t, []correlatedUser{{ID: 2, Email: "bob@example.com"}}, remaining,
			"only the user with no order may survive, so the subquery read each user's own row")
	})

	t.Run("an UPDATE's WHERE clause", func(t *testing.T) {
		users, orders := createCorrelatedFixture(t, db)
		usersRelation, usersID, usersEmail, usersOrderCount := correlatedUserColumns(t, users)
		userProjection := correlatedUserProjection(t, usersID, usersEmail, usersOrderCount)
		usersRef := users.Ref()

		statement, err := query.NewUpdate(usersRef, query.Set(usersRef.Column("email"), "buyer@example.com"))
		require.NoError(t, err)
		statement, err = statement.WithWhere(query.Exists(correlatedOrdersOfUser(t, users, orders, orders.Ref().Column("id"))))
		require.NoError(t, err)
		correlatedExecStatement(t, executor, statement)

		updated := correlatedAllUsers(t, executor, usersRelation, userProjection, usersID)
		require.Equal(t, []correlatedUser{
			{ID: 1, Email: "buyer@example.com"},
			{ID: 2, Email: "bob@example.com"},
			{ID: 3, Email: "buyer@example.com"},
		}, updated, "the user with no order must keep the address it had")
	})

	t.Run("an UPDATE's SET assignment value", func(t *testing.T) {
		users, orders := createCorrelatedFixture(t, db)
		usersRelation, usersID, usersEmail, usersOrderCount := correlatedUserColumns(t, users)
		userProjection := correlatedUserProjection(t, usersID, usersEmail, usersOrderCount)
		usersRef := users.Ref()

		counted := correlatedOrdersOfUser(t, users, orders, query.Project(query.CountAll()))
		statement, err := query.NewUpdate(usersRef, query.Set(usersRef.Column("order_count"), query.Scalar(counted)))
		require.NoError(t, err)
		statement, err = statement.AllowAll()
		require.NoError(t, err)
		correlatedExecStatement(t, executor, statement)

		counts := correlatedAllUsers(t, executor, usersRelation, userProjection, usersID)
		require.Equal(t, []correlatedUser{
			{ID: 1, Email: "ada@example.com", OrderCount: 2},
			{ID: 2, Email: "bob@example.com", OrderCount: 0},
			{ID: 3, Email: "cyd@example.com", OrderCount: 1},
		}, counts, "each row must carry the count of its own orders, not one count shared by all three")
	})
}

// correlatedOrdersOfUser builds SELECT projection FROM orders WHERE
// orders.user_id = users.id, correlated with users, which is the subquery every
// correlated write test above runs.
func correlatedOrdersOfUser(t *testing.T, users rasql.Table[correlatedUser], orders rasql.Table[correlatedOrder], projection query.Projection) query.Select {
	t.Helper()

	statement, err := query.NewSelect(orders.Ref(), projection)
	require.NoError(t, err)
	statement, err = statement.WithCorrelation(users.Ref())
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(orders.Ref().Column("user_id"), users.Ref().Column("id")))
	require.NoError(t, err)
	return statement
}
