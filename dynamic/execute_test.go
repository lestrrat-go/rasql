package dynamic_test

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

func TestDBQueryExecutesParameterizedSelect(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	sequence, err := dynamic.Query(t.Context(), db, statement)
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)

	gotID, err := dynamic.Get[int64](rows[0], "id")
	require.NoError(t, err)
	gotEmail, err := dynamic.Get[string](rows[0], "email")
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	require.Equal(t, "ada@example.com", gotEmail)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSelectBuilderExecutesQuery(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := dynamicUsersTable(t)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	sequence, err := dynamic.SelectFrom(users).
		Select("id", "email").
		WhereEqual("id", 42).
		Query(t.Context(), db)
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSelectBuilderWhereInMatchesRenderSelectFrom(t *testing.T) {
	users := dynamicUsersTable(t)

	fromRender, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id", "email").
		WhereIn("id", 1, 2).
		Build()
	require.NoError(t, err)

	db, err := rasql.New(&debugQueryer{}, dialect.PostgreSQL())
	require.NoError(t, err)
	fromClient, err := dynamic.SelectFrom(users).
		Select("id", "email").
		WhereIn("id", 1, 2).
		Build(db.Dialect())
	require.NoError(t, err)

	require.Equal(t, fromRender.SQL(), fromClient.SQL())
	require.Equal(t, fromRender.Args(), fromClient.Args())
}

func TestSelectBuilderCountReturnsRowCount(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := dynamicUsersTable(t)
	mock.ExpectQuery("SELECT COUNT(*) AS \"count\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))

	count, err := dynamic.SelectFrom(users).WhereEqual("id", 42).Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSelectBuilderCountRejectsWrongRowCount(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := dynamicUsersTable(t)
	// Count reports the shared single-row sentinels, the same ones rasql's
	// single-row terminals report, rather than a message of its own.
	mock.ExpectQuery("SELECT COUNT(*) AS \"count\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}))
	mock.ExpectQuery("SELECT COUNT(*) AS \"count\" FROM \"users\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)).AddRow(int64(2)))

	_, err = dynamic.SelectFrom(users).Count(t.Context(), db)
	require.ErrorIs(t, err, rasql.ErrNoRows)

	_, err = dynamic.SelectFrom(users).Count(t.Context(), db)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDBQueryAllowsDebugQueryer(t *testing.T) {
	queryer := &debugQueryer{}
	db, err := rasql.New(queryer, dialect.PostgreSQL())
	require.NoError(t, err)

	sequence, err := dynamic.Query(t.Context(), db, selectStatement(t))
	rows := collectRows(t, sequence, err)
	require.Empty(t, rows)
	require.Equal(t, "SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)", queryer.query)
	require.Equal(t, []any{42}, queryer.arguments)
}

func TestDBQueryClosesRowsWhenIterationStops(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(42), "ada@example.com").
			AddRow(int64(43), "bob@example.com")).
		RowsWillBeClosed()

	rows, err := dynamic.Query(t.Context(), db, statement)
	require.NoError(t, err)
	for result, err := range rows {
		require.NoError(t, err)
		id, err := dynamic.Get[int64](result, "id")
		require.NoError(t, err)
		require.Equal(t, int64(42), id)
		break
	}
}

func TestDBQueryReturnsExecutionError(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := selectStatement(t)
	expected := errors.New("database unavailable")
	mock.ExpectQuery("SELECT \"users\".\"id\", \"users\".\"email\" FROM \"users\" WHERE (\"users\".\"id\" = $1)").
		WithArgs(42).
		WillReturnError(expected)

	// The statement runs when the sequence is ranged, not when Query returns,
	// so an execution error arrives as the sequence's one yielded error rather
	// than as Query's own. That is what keeps an abandoned sequence from
	// opening a cursor.
	rows, err := dynamic.Query(t.Context(), db, statement)
	require.NoError(t, err)
	yielded := 0
	for _, err := range rows {
		require.ErrorIs(t, err, expected)
		yielded++
	}
	require.Equal(t, 1, yielded)
}

func TestDBQueryWriteReturnsRows(t *testing.T) {
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
	mock.ExpectQuery("INSERT INTO \"users\" (\"email\") VALUES ($1) RETURNING \"id\", \"email\"").
		WithArgs("ada@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	sequence, err := dynamic.QueryWrite(t.Context(), db, statement)
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)

	gotID, err := dynamic.Get[int64](rows[0], "id")
	require.NoError(t, err)
	gotEmail, err := dynamic.Get[string](rows[0], "email")
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	require.Equal(t, "ada@example.com", gotEmail)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDBQueryWriteRejectsStatementWithoutReturning(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users := dynamicUsersTable(t)
	email := users.Column("email")
	statement, err := query.NewInsert(users, []query.ColumnRef{email}, []query.Expression{query.Bind("ada@example.com")})
	require.NoError(t, err)

	_, err = dynamic.QueryWrite(t.Context(), db, statement)
	require.ErrorContains(t, err, "rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
}

func TestDBQueryWriteRejectsUnsupportedDialect(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.MySQL())
	require.NoError(t, err)
	statement := insertReturningStatement(t)

	_, err = dynamic.QueryWrite(t.Context(), db, statement)
	require.ErrorContains(t, err, "RETURNING is not supported")
}

// TestEveryDynamicEntryPointRejectsAZeroDB checks that the zero rasql.DB{} is
// refused by every one of this package's terminals, before any of them
// render or execute anything. It is the dynamic-package counterpart to
// root's TestEveryEntryPointRejectsAZeroDB, which covers rasql.Exec,
// rasql.SelectFrom(...).All, and rasql.CreateTable instead.
func TestEveryDynamicEntryPointRejectsAZeroDB(t *testing.T) {
	var zero rasql.DB
	users := dynamicUsersTable(t)
	id := users.Column("id")

	_, err := dynamic.Query(t.Context(), zero, selectStatement(t))
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = dynamic.QueryWrite(t.Context(), zero, insertReturningStatement(t))
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = dynamic.SelectFrom(users).Query(t.Context(), zero)
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = dynamic.DeleteFrom(users).WhereEqual(id, 1).Exec(t.Context(), zero)
	require.ErrorContains(t, err, "rasql: invalid DB")

	_, err = dynamic.DeleteFrom(users).WhereEqual(id, 1).Returning(id).Query(t.Context(), zero)
	require.ErrorContains(t, err, "rasql: invalid DB")
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

func TestQueryRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	db, mock := leakTestDB(t)
	users := dynamicUsersTable(t)
	id := users.Column("id")
	statement, err := query.NewSelect(users, id)
	require.NoError(t, err)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`SELECT "users"."id" FROM "users"`).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42))).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[dynamic.Row, error], error) {
			return dynamic.Query(t.Context(), db, statement)
		})
}

func TestQueryWriteRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	db, mock := leakTestDB(t)
	users := dynamicUsersTable(t)
	id := users.Column("id")
	email := users.Column("email")
	insert, err := query.NewInsert(users, []query.ColumnRef{email}, []query.Expression{query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err := insert.WithReturning(id)
	require.NoError(t, err)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`INSERT INTO "users" ("email") VALUES ($1) RETURNING "id"`).
				WithArgs("ada@example.com").
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42))).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[dynamic.Row, error], error) {
			return dynamic.QueryWrite(t.Context(), db, statement)
		})
}

func TestSelectBuilderQueryRunsNothingUntilTheSequenceIsRanged(t *testing.T) {
	db, mock := leakTestDB(t)
	users := dynamicUsersTable(t)

	requireLazyRowSequence(t, mock,
		func() {
			mock.ExpectQuery(`SELECT "users"."id" FROM "users"`).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42))).
				RowsWillBeClosed()
		},
		func() (iter.Seq2[dynamic.Row, error], error) {
			return dynamic.SelectFrom(users).Select("id").Query(t.Context(), db)
		})
}

// insertReturningStatement builds an INSERT that returns id and email, the
// statement the QueryWrite rejection tests share.
func insertReturningStatement(t *testing.T) query.Insert {
	t.Helper()
	users := dynamicUsersTable(t)
	id := users.Column("id")
	email := users.Column("email")
	statement, err := query.NewInsert(users, []query.ColumnRef{email}, []query.Expression{query.Bind("ada@example.com")})
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, email)
	require.NoError(t, err)
	return statement
}

func selectStatement(t *testing.T) query.Select {
	t.Helper()
	users := dynamicUsersTable(t)
	id := users.Column("id")
	email := users.Column("email")
	statement, err := query.NewSelect(users, id, email)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	return statement
}

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

func collectRows(t *testing.T, sequence iter.Seq2[dynamic.Row, error], queryError error) []dynamic.Row {
	t.Helper()
	require.NoError(t, queryError)
	result := make([]dynamic.Row, 0)
	for value, err := range sequence {
		require.NoError(t, err)
		result = append(result, value)
	}
	return result
}
