package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type deleteUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

func deleteUsersTable(t *testing.T) rasql.Table[deleteUser] {
	t.Helper()

	users, err := rasql.TableOf[deleteUser](schema.TableDef{
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

func TestDeleteRejectsNilPredicate(t *testing.T) {
	users := deleteUsersTable(t)
	d := dbForBuild(t).Dialect()
	var typedNil *query.Binary
	for _, predicate := range []struct {
		name       string
		expression query.Expression
	}{
		{name: "nil interface"},
		{name: "typed nil", expression: typedNil},
	} {
		t.Run(predicate.name, func(t *testing.T) {
			_, err := rasql.DeleteFrom(users).Where(predicate.expression).Build(d)
			require.ErrorContains(t, err, "WHERE expression must not be nil")
		})
	}
}

func TestDeleteFrom(t *testing.T) {
	t.Run("WhereEqual deletes matching rows", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		mock.ExpectExec("DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1)").
			WithArgs(42).
			WillReturnResult(sqlmock.NewResult(0, 1))

		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		result, err := rasql.DeleteFrom(users).WhereEqual(id, 42).Exec(t.Context(), db)
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
	})

	t.Run("WhereIn deletes matching rows", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		mock.ExpectExec("DELETE FROM \"users\" WHERE (\"users\".\"id\" IN ($1, $2))").
			WithArgs(1, 2).
			WillReturnResult(sqlmock.NewResult(0, 2))

		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		result, err := rasql.DeleteFrom(users).WhereIn(id, 1, 2).Exec(t.Context(), db)
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 2, rows)
	})

	t.Run("WhereIn with no values reports an error", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		_, err = rasql.DeleteFrom(users).WhereIn(id).Build(dbForBuild(t).Dialect())
		require.EqualError(t, err, "rasql: IN requires at least one value")
	})

	t.Run("WhereIn with a column from another table reports an error", func(t *testing.T) {
		other, err := rasql.TableOf[deleteUser](schema.TableDef{
			Name:       "archived_users",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		archivedID, err := other.Column("id")
		require.NoError(t, err)

		_, err = rasql.DeleteFrom(deleteUsersTable(t)).WhereIn(archivedID, 1).Build(dbForBuild(t).Dialect())
		require.ErrorContains(t, err, "archived_users")
	})

	t.Run("Where takes a query expression", func(t *testing.T) {
		users := deleteUsersTable(t)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := rasql.DeleteFrom(users).
			Where(query.Equal(email, query.Bind("ada@example.com"))).
			Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t, "DELETE FROM \"users\" WHERE (\"users\".\"email\" = $1)", statement.SQL())
		require.Equal(t, []any{"ada@example.com"}, statement.Args())
	})

	t.Run("no predicate is rejected at Build", func(t *testing.T) {
		_, err := rasql.DeleteFrom(deleteUsersTable(t)).Build(dbForBuild(t).Dialect())
		require.ErrorContains(t, err, "requires a WHERE predicate or an explicit AllowAll")
	})

	t.Run("no predicate is rejected at Exec", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)

		_, err = rasql.DeleteFrom(deleteUsersTable(t)).Exec(t.Context(), db)
		require.ErrorContains(t, err, "requires a WHERE predicate or an explicit AllowAll")
	})

	t.Run("WhereIn alone satisfies the predicate requirement", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)

		statement, err := rasql.DeleteFrom(users).WhereIn(id, 1, 2).Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t, "DELETE FROM \"users\" WHERE (\"users\".\"id\" IN ($1, $2))", statement.SQL())
		require.Equal(t, []any{1, 2}, statement.Args())
	})

	t.Run("AllowAll builds a full-table delete", func(t *testing.T) {
		statement, err := rasql.DeleteFrom(deleteUsersTable(t)).AllowAll().Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t, "DELETE FROM \"users\"", statement.SQL())
		require.Empty(t, statement.Args())
	})

	t.Run("AllowAll executes", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		mock.ExpectExec("DELETE FROM \"users\"").
			WillReturnResult(sqlmock.NewResult(0, 3))

		result, err := rasql.DeleteFrom(deleteUsersTable(t)).AllowAll().Exec(t.Context(), db)
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 3, rows)
	})

	t.Run("AllowAll with a predicate is rejected", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)

		t.Run("AllowAll then WhereEqual", func(t *testing.T) {
			_, err := rasql.DeleteFrom(users).AllowAll().WhereEqual(id, 42).Build(dbForBuild(t).Dialect())
			require.ErrorContains(t, err, "must not be combined")
		})

		t.Run("WhereEqual then AllowAll", func(t *testing.T) {
			_, err := rasql.DeleteFrom(users).WhereEqual(id, 42).AllowAll().Build(dbForBuild(t).Dialect())
			require.ErrorContains(t, err, "must not be combined")
		})

		t.Run("WhereIn then AllowAll", func(t *testing.T) {
			_, err := rasql.DeleteFrom(users).WhereIn(id, 42).AllowAll().Build(dbForBuild(t).Dialect())
			require.ErrorContains(t, err, "must not be combined")
		})
	})

	t.Run("column from another table reports an error", func(t *testing.T) {
		other, err := rasql.TableOf[deleteUser](schema.TableDef{
			Name:       "archived_users",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		archivedID, err := other.Column("id")
		require.NoError(t, err)

		_, err = rasql.DeleteFrom(deleteUsersTable(t)).WhereEqual(archivedID, 1).Build(dbForBuild(t).Dialect())
		require.ErrorContains(t, err, "archived_users")
	})

	t.Run("repeated Where combines with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := rasql.DeleteFrom(users).
			Where(query.Equal(id, query.Bind(42))).
			Where(query.Equal(email, query.Bind("ada@example.com"))).
			Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`DELETE FROM "users" WHERE (("users"."id" = $1) AND ("users"."email" = $2))`,
			statement.SQL())
		require.Equal(t, []any{42, "ada@example.com"}, statement.Args())
	})

	t.Run("WhereEqual after Where combines with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := rasql.DeleteFrom(users).
			Where(query.Equal(email, query.Bind("ada@example.com"))).
			WhereEqual(id, 42).
			Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`DELETE FROM "users" WHERE (("users"."email" = $1) AND ("users"."id" = $2))`,
			statement.SQL())
		require.Equal(t, []any{"ada@example.com", 42}, statement.Args())
	})

	t.Run("WhereIn after WhereEqual combines with AND", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)
		statement, err := rasql.DeleteFrom(users).
			WhereEqual(email, "ada@example.com").
			WhereIn(id, 1, 2).
			Build(dbForBuild(t).Dialect())
		require.NoError(t, err)
		require.Equal(t,
			`DELETE FROM "users" WHERE (("users"."email" = $1) AND ("users"."id" IN ($2, $3)))`,
			statement.SQL())
		require.Equal(t, []any{"ada@example.com", 1, 2}, statement.Args())
	})

	t.Run("derived builders do not share predicates", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)

		// Three predicates leave the accumulated slice with spare backing-array
		// capacity (Go grows a nil slice 0 -> 1 -> 2 -> 4), which is what exposes
		// the aliasing hazard DeleteBuilder.clone must guard against.
		base := rasql.DeleteFrom(users).
			WhereEqual(id, 1).
			WhereEqual(id, 2).
			WhereEqual(id, 3)

		first := base.Where(query.Equal(email, query.Bind("ada@example.com")))
		second := base.Where(query.Equal(email, query.Bind("bob@example.com")))

		d := dbForBuild(t).Dialect()
		firstStatement, err := first.Build(d)
		require.NoError(t, err)
		secondStatement, err := second.Build(d)
		require.NoError(t, err)

		require.Contains(t, firstStatement.Args(), "ada@example.com")
		require.NotContains(t, firstStatement.Args(), "bob@example.com")

		require.Contains(t, secondStatement.Args(), "bob@example.com")
		require.NotContains(t, secondStatement.Args(), "ada@example.com")
	})

	t.Run("a column from another table still errors after a valid predicate", func(t *testing.T) {
		users := deleteUsersTable(t)
		id, err := users.Column("id")
		require.NoError(t, err)
		other, err := rasql.TableOf[deleteUser](schema.TableDef{
			Name:       "archived_users",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		archivedID, err := other.Column("id")
		require.NoError(t, err)

		_, err = rasql.DeleteFrom(users).
			WhereEqual(id, 1).
			WhereEqual(archivedID, 1).
			Build(dbForBuild(t).Dialect())
		require.ErrorContains(t, err, "archived_users")
	})
}

func TestDeleteReturningBuild(t *testing.T) {
	users := deleteUsersTable(t)
	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)

	statement, err := rasql.DeleteFrom(users).
		WhereEqual(id, 42).
		Returning(query.Project(id), query.Project(email)).
		Build(dbForBuild(t).Dialect())
	require.NoError(t, err)
	require.Equal(t, `DELETE FROM "users" WHERE ("users"."id" = $1) RETURNING "id", "email"`, statement.SQL())
	require.Equal(t, []any{42}, statement.Args())
}

func TestDeleteReturningTypedHelpers(t *testing.T) {
	t.Run("all", func(t *testing.T) {
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
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)
		mock.ExpectQuery("DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1) RETURNING \"id\", \"email\"").
			WithArgs(42).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

		rows, err := rasql.QueryDeleteAll[deleteUser](
			t.Context(),
			db,
			rasql.DeleteFrom(users).WhereEqual(id, 42).Returning(query.Project(id), query.Project(email)),
		)
		require.NoError(t, err)
		require.Equal(t, []deleteUser{{ID: 42, Email: "ada@example.com"}}, rows)
	})

	t.Run("one", func(t *testing.T) {
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
		id, err := users.Column("id")
		require.NoError(t, err)
		email, err := users.Column("email")
		require.NoError(t, err)
		mock.ExpectQuery("DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1) RETURNING \"id\", \"email\"").
			WithArgs(42).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

		user, err := rasql.QueryDeleteOne[deleteUser](
			t.Context(),
			db,
			rasql.DeleteFrom(users).WhereEqual(id, 42).Returning(query.Project(id), query.Project(email)),
		)
		require.NoError(t, err)
		require.Equal(t, deleteUser{ID: 42, Email: "ada@example.com"}, user)
	})
}

func TestDeleteReturningRejectsUnsupportedDialect(t *testing.T) {
	users := deleteUsersTable(t)
	id, err := users.Column("id")
	require.NoError(t, err)

	_, err = rasql.DeleteFrom(users).
		WhereEqual(id, 42).
		Returning(query.Project(id)).
		Build(dialect.MySQL())
	require.ErrorContains(t, err, "RETURNING is not supported")
}

func TestDeleteReturningRequiresProjection(t *testing.T) {
	users := deleteUsersTable(t)
	id, err := users.Column("id")
	require.NoError(t, err)

	_, err = rasql.DeleteFrom(users).WhereEqual(id, 42).Returning().Build(dialect.PostgreSQL())
	require.EqualError(t, err, "rasql: RETURNING requires at least one projection")
}

// dbForBuild returns a DB that renders statements without executing them.
func dbForBuild(t *testing.T) rasql.DB {
	t.Helper()

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return db
}
