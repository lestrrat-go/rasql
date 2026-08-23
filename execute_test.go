package rasql_test

import (
	"context"
	"database/sql"
	"iter"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestTypedSelectBuilderRunsSubqueryPredicate(t *testing.T) {
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
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")
	email := users.Column("email")

	orders, err := query.NewTableRef(schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orderUserID := orders.Column("user_id")
	orderStatus := orders.Column("status")

	activeOrders, err := query.NewSelect(orders, orderUserID)
	require.NoError(t, err)
	activeOrders, err = activeOrders.WithWhere(query.Equal(orderStatus, query.Bind(7)))
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."id" IN (SELECT "orders"."user_id" FROM "orders" WHERE ("orders"."status" = $1)))`).
		WithArgs(7).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "ada@example.com").
			AddRow(int64(2), "bob@example.com"))

	rows, err := rasql.DecodeFrom[user](users).
		Project(id, email).
		Where(query.InSelect(id, activeOrders)).
		Query(t.Context(), db)
	require.NoError(t, err)
	decoded := make([]user, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []user{{ID: 1, Email: "ada@example.com"}, {ID: 2, Email: "bob@example.com"}}, decoded)
}

func TestTypedSelectFromDecodesGeneratedRowType(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](table)
	require.NoError(t, err)
	id := users.Column("id")
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	rows, err := rasql.SelectFrom(users).WhereEqual(id, 42).Query(t.Context(), db)
	require.NoError(t, err)
	decoded := make([]user, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []user{{ID: 42, Email: "ada@example.com"}}, decoded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDecodeFromRefDecodesProjectedRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")
	email := users.Column("email")
	mock.ExpectQuery("SELECT \"users\".\"id\" AS \"user_id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email"}).AddRow(int64(42), "ada@example.com"))

	type summary struct {
		UserID int64
		Email  string
	}
	rows, err := rasql.DecodeFromRef[summary](users).
		Project(id.As("user_id"), email).
		Where(query.Equal(id, query.Bind(42))).
		Query(t.Context(), db)
	require.NoError(t, err)
	decoded := make([]summary, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []summary{{UserID: 42, Email: "ada@example.com"}}, decoded)
}

// TestDecodeFromDecodesGroupedRows proves a grouped aggregate query reaches a
// typed caller: GroupBy and Having on TypedSelectBuilder render into the
// statement DecodeFrom builds, and the two result columns decode into a
// two-field result struct.
func TestDecodeFromDecodesGroupedRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type task struct {
		ID     int64  `rasql:"id"`
		Status string `rasql:"status"`
	}
	tasks, err := rasql.TableOf[task](schema.TableDef{
		Name: "tasks",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	status := tasks.Column("status")

	type statusCount struct {
		Status string `rasql:"status"`
		Total  int64  `rasql:"total"`
	}

	mock.ExpectQuery("SELECT \"tasks\".\"status\", COUNT(*) AS \"total\" FROM \"tasks\" GROUP BY \"tasks\".\"status\" HAVING (COUNT(*) > $1)").
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"status", "total"}).
			AddRow("open", int64(3)).
			AddRow("done", int64(5)))

	rows, err := rasql.DecodeFrom[statusCount](tasks).
		Project(status, query.CountAll().As("total")).
		GroupBy(status).
		Having(query.GreaterThan(query.CountAll(), query.Bind(1))).
		Query(t.Context(), db)
	require.NoError(t, err)
	decoded := make([]statusCount, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []statusCount{{Status: "open", Total: 3}, {Status: "done", Total: 5}}, decoded)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDecodeFromDecodesDistinctRows proves Distinct on TypedSelectBuilder
// renders into the statement DecodeFrom builds and reaches a typed caller,
// the distinct counterpart to TestDecodeFromDecodesGroupedRows.
func TestDecodeFromDecodesDistinctRows(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type task struct {
		ID     int64  `rasql:"id"`
		Status string `rasql:"status"`
	}
	tasks, err := rasql.TableOf[task](schema.TableDef{
		Name: "tasks",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	status := tasks.Column("status")

	type statusOnly struct {
		Status string `rasql:"status"`
	}

	mock.ExpectQuery("SELECT DISTINCT \"tasks\".\"status\" FROM \"tasks\"").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).
			AddRow("open").
			AddRow("done"))

	rows, err := rasql.DecodeFrom[statusOnly](tasks).
		Project(status).
		Distinct().
		Query(t.Context(), db)
	require.NoError(t, err)
	decoded := make([]statusOnly, 0)
	for value, err := range rows {
		require.NoError(t, err)
		decoded = append(decoded, value)
	}
	require.Equal(t, []statusOnly{{Status: "open"}, {Status: "done"}}, decoded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTypedSelectBuilderCountReturnsRowCount(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](table)
	require.NoError(t, err)
	id := users.Column("id")
	// Count must not select the table's columns, unlike every other typed
	// query; only COUNT(*) AS "count" reaches the database.
	mock.ExpectQuery("SELECT COUNT(*) AS \"count\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))

	count, err := rasql.SelectFrom(users).WhereEqual(id, 42).Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTypedSelectBuilderCountReadsANonIntegerCount pins the countRow path's
// conversion, which is where the rebuild onto render.SelectBuilder changed
// behavior: Count now scans through database/sql's own conversion
// (convertAssign) rather than through the reflective dynamic.Get[int64] the
// untyped builder still uses, and that conversion accepts a string or a
// []byte for an integer destination where the reflective path does not.
func TestTypedSelectBuilderCountReadsANonIntegerCount(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value any
	}{
		{name: "string", value: "3"},
		{name: "[]byte", value: []byte("3")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
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
				ID    int64  `rasql:"id"`
				Email string `rasql:"email"`
			}
			users, err := rasql.TableOf[user](schema.TableDef{
				Name: "users",
				Columns: []schema.ColumnDef{
					{Name: "id", Type: schema.IntegerType{}},
					{Name: "email", Type: schema.TextType{}},
				},
				PrimaryKey: []string{"id"},
			})
			require.NoError(t, err)
			mock.ExpectQuery("SELECT COUNT(*) AS \"count\" FROM \"users\"").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(testCase.value))

			count, err := rasql.SelectFrom(users).Count(t.Context(), db)
			require.NoError(t, err)
			require.Equal(t, int64(3), count)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestTypedSelectBuilderRejectsEmptyIn pins the new err field TypedSelectBuilder
// carries directly since the rebuild onto render.SelectBuilder: WhereIn's empty
// value list no longer travels through the untyped builder's own error state.
func TestTypedSelectBuilderRejectsEmptyIn(t *testing.T) {
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")

	_, err = rasql.SelectFrom(users).WhereIn(id).Build(dbForBuild(t).Dialect())
	require.EqualError(t, err, "rasql: IN requires at least one value")

	_, err = rasql.SelectFrom(users).WhereIn(id).All(t.Context(), dbForBuild(t))
	require.EqualError(t, err, "rasql: render SELECT: rasql: IN requires at least one value")
}

func TestDBExecExecutesParameterizedInsert(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")
	email := users.Column("email")
	statement, err := query.NewInsert(users, query.Set(id, 42), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(42, "ada@example.com").
		WillReturnResult(sqlmock.NewResult(42, 1))

	result, err := rasql.Exec(t.Context(), db, statement)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDBQueryRenderedExecutesStaticStatement(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	s := stmt.New("SELECT id FROM users WHERE id = $1", 42)
	mock.ExpectQuery("SELECT id FROM users WHERE id = $1").WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))

	sqlRows, err := db.QueryRendered(t.Context(), s)
	rows := collectRows(t, rowvalue.Scan(sqlRows), err)
	require.Len(t, rows, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateExecutesTableAndIndexes(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "users_email_idx",
			Columns: []string{"email"},
		}},
	}
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](table)
	require.NoError(t, err)
	mock.ExpectExec("CREATE TABLE \"users\" (\"id\" BIGINT NOT NULL, \"email\" TEXT NOT NULL, PRIMARY KEY (\"id\"))").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE INDEX \"users_email_idx\" ON \"users\" (\"email\")").
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestEveryEntryPointRejectsAZeroDB checks that the zero DB{} is refused by
// the functions that take one, before they render or execute anything.
// dynamic.Query and dynamic.QueryWrite have the equivalent check in
// dynamic/execute_test.go, since neither lives in this package anymore.
func TestEveryEntryPointRejectsAZeroDB(t *testing.T) {
	var zero rasql.DB
	users := deleteUsersTable(t)

	_, err := rasql.Exec(t.Context(), zero, insertStatement(t))
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = rasql.SelectFrom(users).All(t.Context(), zero)
	require.ErrorContains(t, err, "rasql: invalid DB")

	require.ErrorContains(t, rasql.CreateTable(t.Context(), zero, users), "rasql: invalid DB")
}

func TestDBExecRejectsReturningStatement(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := insertReturningStatement(t)

	_, err = rasql.Exec(t.Context(), db, statement)
	require.ErrorContains(t, err, "rasql: write statement has a RETURNING clause: use QueryWrite to read its rows")
}

func TestDBExecStillAcceptsStatementWithoutReturning(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement, err := query.NewInsert(usersWriteTable(t), query.Set(usersEmailColumn(t), "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"email\") VALUES ($1)").
		WithArgs("ada@example.com").
		WillReturnResult(sqlmock.NewResult(42, 1))

	_, err = rasql.Exec(t.Context(), db, statement)
	require.NoError(t, err)
}

func TestExecRejectsUnconditionalMutations(t *testing.T) {
	users := usersWriteTable(t)
	email := users.Column("email")

	tests := []struct {
		name      string
		statement func() (query.WriteStatement, error)
		message   string
	}{
		{
			name: "update",
			statement: func() (query.WriteStatement, error) {
				return query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
			},
			message: "UPDATE requires a WHERE predicate or an explicit AllowAll",
		},
		{
			name: "delete",
			statement: func() (query.WriteStatement, error) {
				return query.NewDelete(users)
			},
			message: "DELETE requires a WHERE predicate or an explicit AllowAll",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			db, err := rasql.New(database, dialect.PostgreSQL())
			require.NoError(t, err)
			statement, err := testCase.statement()
			require.NoError(t, err)

			_, err = rasql.Exec(t.Context(), db, statement)
			require.ErrorContains(t, err, testCase.message)
		})
	}
}

func TestExecRunsTargetedAndExplicitlyAllowedMutations(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := usersWriteTable(t)
	id := users.Column("id")
	email := users.Column("email")

	targetedUpdate, err := query.NewUpdate(users, query.Set(email, query.Bind("grace@example.com")))
	require.NoError(t, err)
	targetedUpdate, err = targetedUpdate.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	targetedDelete, err := query.NewDelete(users)
	require.NoError(t, err)
	targetedDelete, err = targetedDelete.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	allowedUpdate, err := query.NewUpdate(users, query.Set(email, query.Bind("ada@example.com")))
	require.NoError(t, err)
	allowedUpdate, err = allowedUpdate.AllowAll()
	require.NoError(t, err)
	allowedDelete, err := query.NewDelete(users)
	require.NoError(t, err)
	allowedDelete, err = allowedDelete.AllowAll()
	require.NoError(t, err)

	mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
		WithArgs("grace@example.com", 42).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1").
		WithArgs("ada@example.com").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("DELETE FROM \"users\"").
		WillReturnResult(sqlmock.NewResult(0, 3))

	_, err = rasql.Exec(t.Context(), db, targetedUpdate)
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, targetedDelete)
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, allowedUpdate)
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, allowedDelete)
	require.NoError(t, err)
}

// usersWriteTable returns the write target the returning-related tests share.
func usersWriteTable(t *testing.T) query.TableRef {
	t.Helper()
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}

func usersEmailColumn(t *testing.T) query.ColumnRef {
	t.Helper()
	email := usersWriteTable(t).Column("email")
	return email
}

// insertReturningStatement builds an INSERT that returns id and email, the
// statement the Exec rejection test shares.
func insertReturningStatement(t *testing.T) query.Insert {
	t.Helper()
	users := usersWriteTable(t)
	id := users.Column("id")
	email := users.Column("email")
	statement, err := query.NewInsert(users, query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, email)
	require.NoError(t, err)
	return statement
}

// insertStatement builds the same INSERT without a RETURNING clause, the one
// Exec accepts.
func insertStatement(t *testing.T) query.Insert {
	t.Helper()
	users := usersWriteTable(t)
	email := users.Column("email")
	statement, err := query.NewInsert(users, query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	return statement
}

// leakTestDB returns a DB over a mock whose expectations are checked when the
// test ends.
func leakTestDB(t *testing.T) (rasql.DB, sqlmock.Sqlmock) {
	t.Helper()

	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return db, mock
}

// requireLazyRowSequence asserts that obtaining a row sequence and abandoning it
// without ranging leaves no cursor open, and that ranging one to completion
// closes the rows it opens.
//
// expect registers exactly one query expectation marked RowsWillBeClosed, and
// obtain returns a fresh sequence for that query. The expectation is registered
// once and consumed by the ranged pass, so the abandoned pass leaves it
// unmatched: that unmatched expectation is the proof that no query ran and
// therefore no rows were opened. An implementation that queries before
// returning the sequence matches the expectation during the abandoned pass and
// leaves its rows open, which ExpectationsWereMet reports as
// "expected query rows to be closed, but it was not".
func requireLazyRowSequence[T any](t *testing.T, mock sqlmock.Sqlmock, expect func(), obtain func() (iter.Seq2[T, error], error)) {
	t.Helper()

	expect()

	abandoned, err := obtain()
	require.NoError(t, err)
	require.NotNil(t, abandoned)
	require.ErrorContains(t, mock.ExpectationsWereMet(), "there is a remaining expectation which was not matched")

	ranged, err := obtain()
	require.NoError(t, err)
	count := 0
	for _, err := range ranged {
		require.NoError(t, err)
		count++
	}
	require.Equal(t, 1, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestTypedSelectBuilderQueryRunsNothingUntilTheSequenceIsRanged pins the
// laziness rule directly to TypedSelectBuilder.Query: since the rebuild onto
// render.SelectBuilder, the typed builder owns this rule itself rather than
// inheriting it by delegating to the untyped builder's Query.
func TestTypedSelectBuilderQueryRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	db, mock := leakTestDB(t)
	users := deleteUsersTable(t)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`SELECT "users"."id", "users"."email" FROM "users"`).
				WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com")).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[deleteUser, error], error) {
			return rasql.SelectFrom(users).Query(t.Context(), db)
		})
}

// buildOnlyDB returns a DB over a handle that runs nothing, for tests that
// assert on an error raised while a statement is built, before it would reach
// the database. A zero DB{} cannot serve that purpose: every entry point
// rejects one before it builds anything.
func buildOnlyDB(t *testing.T) rasql.DB {
	t.Helper()
	db, err := rasql.New(&debugQueryer{}, dialect.PostgreSQL())
	require.NoError(t, err)
	return db
}

// debugQueryer is a Handle that logs the statement it is given instead of
// running it. buildOnlyDB above uses it for a handle that runs nothing; the
// tests below use it to prove a logged statement never reaches the database.
type debugQueryer struct {
	query     string
	arguments []any
}

func (q *debugQueryer) QueryContext(_ context.Context, query string, arguments ...any) (*sql.Rows, error) {
	q.query = query
	q.arguments = append([]any(nil), arguments...)
	return nil, nil
}

func (q *debugQueryer) ExecContext(_ context.Context, query string, arguments ...any) (sql.Result, error) {
	q.query = query
	q.arguments = append([]any(nil), arguments...)
	return nil, nil
}

func collectRows(t *testing.T, sequence iter.Seq2[rowvalue.Row, error], queryError error) []rowvalue.Row {
	t.Helper()
	require.NoError(t, queryError)
	result := make([]rowvalue.Row, 0)
	for value, err := range sequence {
		require.NoError(t, err)
		result = append(result, value)
	}
	return result
}
