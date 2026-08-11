package rasql_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type directScanUser struct {
	ID    int64
	Email string
}

type staticScanUser struct {
	ID    int64
	Email string
}

type plannedScanUser struct {
	Name string
}

var plannedScanCalls int

func (u *plannedScanUser) ScanDestinations(columns []string) ([]any, error) {
	plannedScanCalls++
	if len(columns) != 1 || columns[0] != "name" {
		return nil, fmt.Errorf("unexpected result columns %q", columns)
	}
	return []any{&u.Name}, nil
}

func (u *staticScanUser) ScanRow(source row.ScanSource) error {
	return source.Scan(&u.ID, &u.Email)
}

func (u *staticScanUser) DecodeRow(row.Dynamic) error {
	return errors.New("static scanner must bypass DecodeRow")
}

func (u *directScanUser) ScanRow(source row.ScanSource) error {
	return source.Scan(&u.ID, &u.Email)
}

func (u *directScanUser) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	var discard any
	for index, column := range columns {
		switch column {
		case "id":
			destinations[index] = &u.ID
		case "email":
			destinations[index] = &u.Email
		default:
			destinations[index] = &discard
		}
	}
	return destinations, nil
}

func (u *directScanUser) DecodeRow(row.Dynamic) error {
	return errors.New("direct scanner must bypass DecodeRow")
}

func TestTypedSelectScansKnownProjectionDirectly(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[staticScanUser](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(7), "ada@example.com"))

	result, err := rasql.SelectFrom(users).One(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, staticScanUser{ID: 7, Email: "ada@example.com"}, result)
}

func TestTypedSelectMapsPartialGeneratedScanColumns(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[directScanUser](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	mock.ExpectQuery("SELECT \"users\".\"email\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("ada@example.com"))

	result, err := rasql.DecodeFrom[directScanUser](users).
		Project(query.Project(email)).
		One(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, directScanUser{Email: "ada@example.com"}, result)
}

func TestTypedSelectProjectUsesRuntimeColumnMapping(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[directScanUser](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\", $1 AS \"ignored\" FROM \"users\"").
		WithArgs("ignored").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "ignored"}).AddRow(int64(7), "ada@example.com", "ignored"))

	result, err := rasql.SelectFrom(users).
		Project(query.Project(query.Bind("ignored")).As("ignored")).
		One(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, directScanUser{ID: 7, Email: "ada@example.com"}, result)
}

func TestTypedSelectBuildsGeneratedScanDestinationsOnce(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[plannedScanUser](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "name", Type: schema.TextType{}},
		},
	})
	require.NoError(t, err)
	name, err := users.Column("name")
	require.NoError(t, err)
	plannedScanCalls = 0
	mock.ExpectQuery("SELECT \"users\".\"name\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Ada Lovelace").AddRow("Grace Hopper"))

	rows, err := rasql.DecodeFrom[plannedScanUser](users).
		Project(query.Project(name)).
		All(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, []plannedScanUser{{Name: "Ada Lovelace"}, {Name: "Grace Hopper"}}, rows)
	require.Equal(t, 1, plannedScanCalls)
}

func TestTypedSelectOneStopsAfterSecondRow(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	thirdRowError := errors.New("third row was read")
	mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow(int64(1)).
			AddRow(int64(2)).
			AddRow(int64(3)).
			RowError(2, thirdRowError)).
		RowsWillBeClosed()

	_, err = rasql.SelectFrom(users).One(t.Context(), client)
	require.EqualError(t, err, "rasql: expected one row, got more than one")
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	require.NotErrorIs(t, err, rasql.ErrNoRows)
	require.NotErrorIs(t, err, sql.ErrNoRows)
	require.NotErrorIs(t, err, thirdRowError)
}

func TestTypedSelectOneNoRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"})).
		RowsWillBeClosed()

	_, err = rasql.SelectFrom(users).One(t.Context(), client)
	require.EqualError(t, err, "rasql: expected one row, got none")
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NotErrorIs(t, err, rasql.ErrMultipleRows)
}

func TestTypedSelectOneQueryFailureIsNotNoRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	driverError := errors.New("connection reset by peer")
	mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\"").
		WillReturnError(driverError)

	_, err = rasql.SelectFrom(users).One(t.Context(), client)
	require.NotErrorIs(t, err, rasql.ErrNoRows)
	require.NotErrorIs(t, err, sql.ErrNoRows)
	require.ErrorIs(t, err, driverError)
}

func TestOneSentinelsAreDistinct(t *testing.T) {
	require.NotErrorIs(t, rasql.ErrMultipleRows, sql.ErrNoRows)
	require.NotErrorIs(t, rasql.ErrMultipleRows, rasql.ErrNoRows)
}

// TestTypedSelectGroupBy proves TypedSelectBuilder.GroupBy and .Having reach
// Build, and that TypedSelectBuilder.Count reports the grouping error rather
// than running a query, since clientForBuild sets no mock expectation for one.
func TestTypedSelectGroupBy(t *testing.T) {
	users := deleteUsersTable(t)
	email, err := users.Column("email")
	require.NoError(t, err)

	type emailCount struct {
		Email string `rasql:"email"`
		Total int64  `rasql:"total"`
	}

	client := clientForBuild(t)
	statement, err := rasql.DecodeFrom[emailCount](users).
		Project(query.Project(email), query.Project(query.CountAll()).As("total")).
		GroupBy(email).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Build(client.Dialect())
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."email", COUNT(*) AS "total" FROM "users" GROUP BY "users"."email" HAVING (COUNT(*) > $1)`,
		statement.SQL())
	require.Equal(t, []any{1}, statement.Args())

	_, err = rasql.DecodeFrom[emailCount](users).
		GroupBy(email).
		Count(t.Context(), client)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot count a grouped statement")
}

// TestTypedSelectDistinct proves TypedSelectBuilder.Distinct reaches Build,
// and that Count refuses a distinct builder rather than rendering
// SELECT DISTINCT COUNT(*), which always answers 1 instead of the number of
// distinct rows.
func TestTypedSelectDistinct(t *testing.T) {
	users := deleteUsersTable(t)
	email, err := users.Column("email")
	require.NoError(t, err)

	type emailOnly struct {
		Email string `rasql:"email"`
	}

	client := clientForBuild(t)
	statement, err := rasql.DecodeFrom[emailOnly](users).
		Project(query.Project(email)).
		Distinct().
		Build(client.Dialect())
	require.NoError(t, err)
	require.Equal(t, `SELECT DISTINCT "users"."email" FROM "users"`, statement.SQL())

	_, err = rasql.DecodeFrom[emailOnly](users).
		Project(query.Project(email)).
		Distinct().
		Count(t.Context(), client)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot count a distinct statement")
}

// TestTypedSelectGroupByJoinedColumn proves the typed builder shares the fixed
// assembly order: the grouping is validated together with the joins, so a joined
// table's column may be grouped by. Attaching the joins after the first
// validation refused it.
func TestTypedSelectGroupByJoinedColumn(t *testing.T) {
	users := deleteUsersTable(t)
	orders := selectOrdersTable(t)
	id, err := users.QueryTable().Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	type userOrderCount struct {
		UserID int64 `rasql:"user_id"`
		Total  int64 `rasql:"total"`
	}

	statement, err := rasql.DecodeFrom[userOrderCount](users).
		Project(query.Project(orderUserID), query.Project(query.CountAll()).As("total")).
		Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
		GroupBy(orderUserID).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Build(clientForBuild(t).Dialect())
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "orders"."user_id", COUNT(*) AS "total" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id") GROUP BY "orders"."user_id" HAVING (COUNT(*) > $1)`,
		statement.SQL())
	require.Equal(t, []any{1}, statement.Args())
}

func TestTypedSelectCombinesPredicates(t *testing.T) {
	t.Run("WhereEqual then Where combine with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(users).
			WhereEqual(id, 42).
			Where(query.Like(email, query.Bind("%@example.com"))).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" LIKE $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "%@example.com"}, statement.Args())
	})

	t.Run("a lone Or predicate survives unwrapped", func(t *testing.T) {
		users := deleteUsersTable(t)
		email, err := users.Column("email")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(users).
			Where(query.Or(
				query.Equal(email, query.Bind("ada@example.com")),
				query.Equal(email, query.Bind("bob@example.com")),
			)).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."email" = $1) OR ("users"."email" = $2))`,
			statement.SQL())
	})

	t.Run("WhereEqual after a lone Or wraps it in AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(users).
			Where(query.Or(
				query.Equal(email, query.Bind("ada@example.com")),
				query.Equal(email, query.Bind("bob@example.com")),
			)).
			WhereEqual(id, 42).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE ((("users"."email" = $1) OR ("users"."email" = $2)) AND ("users"."id" = $3))`,
			statement.SQL())
		require.Equal(t, []any{"ada@example.com", "bob@example.com", 42}, statement.Args())
	})

	t.Run("WhereIn after WhereEqual combine with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)

		statement, err := rasql.SelectFrom(users).
			WhereEqual(email, "ada@example.com").
			WhereIn(id, 1, 2).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" WHERE (("users"."email" = $1) AND ("users"."id" IN ($2, $3)))`,
			statement.SQL())
		require.Equal(t, []any{"ada@example.com", 1, 2}, statement.Args())
	})

	t.Run("predicates from a joined table combine", func(t *testing.T) {
		users := deleteUsersTable(t)
		type order struct {
			ID     int64 `rasql:"id"`
			UserID int64 `rasql:"user_id"`
		}
		orders, err := rasql.TableOf[order](schema.TableDef{
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "user_id", Type: schema.IntegerType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		userID, err := users.Column("id")
		require.NoError(t, err)
		ordersUserID, err := orders.Column("user_id")
		require.NoError(t, err)

		// WhereEqual takes a primary-table column, Where takes a column of the
		// joined table; accumulating them must not bypass the "column must be
		// in the statement" validation for either.
		statement, err := rasql.SelectFrom(users).
			Join(rasql.InnerJoin(orders, query.Equal(userID, ordersUserID))).
			WhereEqual(userID, 42).
			Where(query.GreaterThan(ordersUserID, query.Bind(0))).
			Build(clientForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "users"."id", "users"."email" FROM "users" INNER JOIN "orders" ON ("users"."id" = "orders"."user_id") WHERE (("users"."id" = $1) AND ("orders"."user_id" > $2))`,
			statement.SQL())
		require.Equal(t, []any{42, 0}, statement.Args())
	})
}

func TestTypedSelectWhereIn(t *testing.T) {
	t.Run("builds an IN predicate", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		type user struct {
			ID int64 `rasql:"id"`
		}
		users, err := rasql.TableOf[user](schema.TableDef{
			Name: "users",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		id, err := users.Column("id")
		require.NoError(t, err)
		mock.ExpectQuery("SELECT \"users\".\"id\" FROM \"users\" WHERE (\"users\".\"id\" IN ($1, $2))").
			WithArgs(1, 2).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)).AddRow(int64(2)))

		rows, err := rasql.SelectFrom(users).WhereIn(id, 1, 2).All(t.Context(), client)
		require.NoError(t, err)
		require.Equal(t, []user{{ID: 1}, {ID: 2}}, rows)
	})

	t.Run("with no values reports an error", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		client, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		type user struct {
			ID int64 `rasql:"id"`
		}
		users, err := rasql.TableOf[user](schema.TableDef{
			Name: "users",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		id, err := users.Column("id")
		require.NoError(t, err)

		_, err = rasql.SelectFrom(users).WhereIn(id).One(t.Context(), client)
		require.ErrorContains(t, err, "at least one value")
	})
}
