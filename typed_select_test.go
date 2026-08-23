package rasql_test

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// directScanUser tags every field out of the field-mapping fallback. Its
// field names would otherwise snake-case to "id" and "email" and match the
// projection, so a passing test would prove nothing about whether ScanRow or
// ScanDestinations actually ran. With every field tagged "-", the fallback has
// no mapped fields left and fails outright, so a successful scan proves the
// direct-scan path ran.
type directScanUser struct {
	ID    int64  `rasql:"-"`
	Email string `rasql:"-"`
}

// staticScanUser tags every field out of the field-mapping fallback for the
// same reason directScanUser does: its field names would otherwise match the
// projection and let the fallback silently succeed even if the scan path broke.
type staticScanUser struct {
	ID    int64  `rasql:"-"`
	Email string `rasql:"-"`
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

func (u *staticScanUser) ScanRow(source rasql.ScanSource) error {
	return source.Scan(&u.ID, &u.Email)
}

func (u *directScanUser) ScanRow(source rasql.ScanSource) error {
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

func TestTypedSelectScansKnownProjectionDirectly(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
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

	result, err := rasql.SelectFrom(users).One(t.Context(), db)
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

	db, err := rasql.New(database, dialect.PostgreSQL())
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
		Project(email).
		One(t.Context(), db)
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

	db, err := rasql.New(database, dialect.PostgreSQL())
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
		One(t.Context(), db)
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

	db, err := rasql.New(database, dialect.PostgreSQL())
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
		Project(name).
		All(t.Context(), db)
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

	db, err := rasql.New(database, dialect.PostgreSQL())
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

	_, err = rasql.SelectFrom(users).One(t.Context(), db)
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

	db, err := rasql.New(database, dialect.PostgreSQL())
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

	_, err = rasql.SelectFrom(users).One(t.Context(), db)
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

	db, err := rasql.New(database, dialect.PostgreSQL())
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

	_, err = rasql.SelectFrom(users).One(t.Context(), db)
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
// than running a query, since dbForBuild sets no mock expectation for one.
func TestTypedSelectGroupBy(t *testing.T) {
	users := deleteUsersTable(t)
	email, err := users.Column("email")
	require.NoError(t, err)

	type emailCount struct {
		Email string `rasql:"email"`
		Total int64  `rasql:"total"`
	}

	db := dbForBuild(t)
	statement, err := rasql.DecodeFrom[emailCount](users).
		Project(email, query.Project(query.CountAll()).As("total")).
		GroupBy(email).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Build(db.Dialect())
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."email", COUNT(*) AS "total" FROM "users" GROUP BY "users"."email" HAVING (COUNT(*) > $1)`,
		statement.SQL())
	require.Equal(t, []any{1}, statement.Args())

	_, err = rasql.DecodeFrom[emailCount](users).
		GroupBy(email).
		Count(t.Context(), db)
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

	db := dbForBuild(t)
	statement, err := rasql.DecodeFrom[emailOnly](users).
		Project(email).
		Distinct().
		Build(db.Dialect())
	require.NoError(t, err)
	require.Equal(t, `SELECT DISTINCT "users"."email" FROM "users"`, statement.SQL())

	_, err = rasql.DecodeFrom[emailOnly](users).
		Project(email).
		Distinct().
		Count(t.Context(), db)
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
	id, err := users.Ref().Column("id")
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)

	type userOrderCount struct {
		UserID int64 `rasql:"user_id"`
		Total  int64 `rasql:"total"`
	}

	statement, err := rasql.DecodeFrom[userOrderCount](users).
		Project(orderUserID, query.Project(query.CountAll()).As("total")).
		Join(query.InnerJoin(orders, query.Equal(id, orderUserID))).
		GroupBy(orderUserID).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Build(dbForBuild(t).Dialect())
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
			Build(dbForBuild(t).Dialect())
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
			Build(dbForBuild(t).Dialect())
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
			Build(dbForBuild(t).Dialect())
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
			Build(dbForBuild(t).Dialect())
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
			Build(dbForBuild(t).Dialect())
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

		db, err := rasql.New(database, dialect.PostgreSQL())
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

		rows, err := rasql.SelectFrom(users).WhereIn(id, 1, 2).All(t.Context(), db)
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

		db, err := rasql.New(database, dialect.PostgreSQL())
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

		_, err = rasql.SelectFrom(users).WhereIn(id).One(t.Context(), db)
		require.ErrorContains(t, err, "at least one value")
	})
}

// The cap() assertions below are deliberately coupled to collectAll's sizing
// rule: a future change to maxCollectPreallocBytes, or to how a LIMIT feeds
// preallocCapacity, is expected to change these numbers, not just the numbers
// in typed_rows_prealloc_test.go.

// TestSelectAllSizesResultFromLimit is the assertion that proves the change:
// a LIMIT pre-sizes the collected slice to the limit, not just to the number
// of rows actually returned.
func TestSelectAllSizesResultFromLimit(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := deleteUsersTable(t)

	mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users" LIMIT $1`).
		WithArgs(8).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "ada@example.com").
			AddRow(int64(2), "bob@example.com").
			AddRow(int64(3), "cy@example.com"))

	got, err := rasql.SelectFrom(users).Limit(8).All(t.Context(), db)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, 8, cap(got))
}

// TestSelectAllCapsTheReservationForAnAbsurdLimit proves the byte-budget
// clamp holds: a LIMIT far beyond any real result must not translate into an
// allocation anywhere near that size.
func TestSelectAllCapsTheReservationForAnAbsurdLimit(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := deleteUsersTable(t)

	mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users" LIMIT $1`).
		WithArgs(10_000_000).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "ada@example.com").
			AddRow(int64(2), "bob@example.com"))

	got, err := rasql.SelectFrom(users).Limit(10_000_000).All(t.Context(), db)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Less(t, cap(got), 10_000_000)
	// maxCollectPreallocBytes = 256 * 1024, defined in typed_rows.go.
	require.LessOrEqual(t, cap(got)*int(reflect.TypeFor[deleteUser]().Size()), 256*1024)
}

// TestSelectAllWithoutLimitReservesNothing proves that a builder with no
// Limit reproduces today's unhinted collectAll behavior: no exact capacity is
// pinned here, since the growth pattern belongs to Go's append, not to this
// package.
func TestSelectAllWithoutLimitReservesNothing(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := deleteUsersTable(t)

	mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "ada@example.com").
			AddRow(int64(2), "bob@example.com"))

	got, err := rasql.SelectFrom(users).All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []deleteUser{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
	}, got)
	require.GreaterOrEqual(t, cap(got), 2)
}

// TestSelectAllReturnsEmptyNotNilForNoRows pins a publicly observable, and
// previously untested, behavior: an empty result decodes to a non-nil empty
// slice, not nil, across every path that reaches collectAll.
func TestSelectAllReturnsEmptyNotNilForNoRows(t *testing.T) {
	t.Run("All with no limit", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users := deleteUsersTable(t)

		mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users"`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))

		got, err := rasql.SelectFrom(users).All(t.Context(), db)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Empty(t, got)
	})

	t.Run("All with Limit(5)", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users := deleteUsersTable(t)

		mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users" LIMIT $1`).
			WithArgs(5).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))

		got, err := rasql.SelectFrom(users).Limit(5).All(t.Context(), db)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Empty(t, got)
	})

	t.Run("QueryRenderedAll", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		s := stmt.New("SELECT id, email FROM users")
		mock.ExpectQuery(s.SQL()).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))

		got, err := rasql.QueryRenderedAll[deleteUser](t.Context(), db, s)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Empty(t, got)
	})
}

// TestSelectAllOffsetDoesNotChangeTheReservation pins the decision that an
// OFFSET is ignored for sizing: it shifts the result window rather than
// widening it, so it must not enter the hint alongside the LIMIT.
func TestSelectAllOffsetDoesNotChangeTheReservation(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := deleteUsersTable(t)

	mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users" LIMIT $1 OFFSET $2`).
		WithArgs(8, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "ada@example.com").
			AddRow(int64(2), "bob@example.com").
			AddRow(int64(3), "cy@example.com"))

	got, err := rasql.SelectFrom(users).Limit(8).Offset(100).All(t.Context(), db)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, 8, cap(got))
}

// selectOrdersTable returns a table to join deleteUsersTable against.
func selectOrdersTable(t *testing.T) query.TableRef {
	t.Helper()

	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return orders
}
