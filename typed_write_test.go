package rasql_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type insertCompatibilityUser struct{}

var _ func(context.Context, rasql.DB, rasql.Table[insertCompatibilityUser], insertCompatibilityUser) (sql.Result, error) = rasql.Insert[insertCompatibilityUser]

func TestInsertExecutesTypedRow(t *testing.T) {
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
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(int64(42), "ada@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := rasql.Insert(t.Context(), db, users, user{ID: 42, Email: "ada@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestInsertManyExecutesOneParameterizedStatement(t *testing.T) {
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
	mock.ExpectExec(`INSERT INTO "users" ("id", "email") VALUES ($1, $2), ($3, $4)`).
		WithArgs(int64(1), "ada@example.com", int64(2), "grace@example.com").
		WillReturnResult(sqlmock.NewResult(1, 2))

	result, err := rasql.InsertMany(t.Context(), db, users, []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "grace@example.com"},
	})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 2, rows)
}

func TestInsertManyWithOptionsUsesDefaultsForEveryRow(t *testing.T) {
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
		ID     int64  `rasql:"id"`
		Email  string `rasql:"email"`
		Status string `rasql:"status"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Default: "next_user_id()"},
			{Name: "email", Type: schema.TextType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	mock.ExpectExec(`INSERT INTO "users" ("email", "status") VALUES ($1, $2), ($3, $4)`).
		WithArgs("ada@example.com", "", "grace@example.com", "").
		WillReturnResult(sqlmock.NewResult(1, 2))

	_, err = rasql.InsertManyWithOptions(
		t.Context(),
		db,
		users,
		[]user{{Email: "ada@example.com"}, {Email: "grace@example.com"}},
		rasql.DefaultColumns("id"),
	)
	require.NoError(t, err)
}

func TestInsertManyWithOptionsUsesDefaultsForEveryColumnForAllDialects(t *testing.T) {
	type user struct {
		ID     int64  `rasql:"id"`
		Status string `rasql:"status"`
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `INSERT INTO "users" DEFAULT VALUES`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "INSERT INTO `users` () VALUES ()",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `INSERT INTO "users" DEFAULT VALUES`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			db, err := rasql.New(database, test.dialect)
			require.NoError(t, err)
			users, err := rasql.TableOf[user](schema.TableDef{
				Name: "users",
				Columns: []schema.ColumnDef{
					{Name: "id", Type: schema.IntegerType{}, Default: "next_user_id()"},
					{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
				},
				PrimaryKey: []string{"id"},
			})
			require.NoError(t, err)
			mock.ExpectExec(test.sql).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec(test.sql).WillReturnResult(sqlmock.NewResult(2, 1))

			result, err := rasql.InsertManyWithOptions(
				t.Context(),
				db,
				users,
				[]user{{}, {}},
				rasql.DefaultColumns("id", "status"),
			)
			require.NoError(t, err)
			rows, err := result.RowsAffected()
			require.NoError(t, err)
			require.EqualValues(t, 2, rows)
		})
	}
}

func TestInsertManyWithOptionsValidatesAllDefaultRows(t *testing.T) {
	type user struct {
		ID     int64  `rasql:"id"`
		Status string `rasql:"status"`
	}
	users, err := rasql.TableOf[*user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Default: "next_user_id()"},
			{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	_, err = rasql.InsertManyWithOptions(
		t.Context(),
		buildOnlyDB(t),
		users,
		[]*user{&user{}, nil},
		rasql.DefaultColumns("id", "status"),
	)
	require.ErrorContains(t, err, `row 1: row value must not be nil`)
}

func TestInsertManyRejectsEmptyRows(t *testing.T) {
	type user struct {
		ID int64 `rasql:"id"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	_, err = rasql.InsertMany(t.Context(), buildOnlyDB(t), users, []user{})
	require.ErrorContains(t, err, "rows must not be empty")
}

// generatedDefaultUser has the method-based mapping rasqlgen writes for a row
// type, including fields whose values come from database defaults on insert.
type generatedDefaultUser struct {
	ID     int64
	Email  string
	Status string
}

func (u generatedDefaultUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	case "status":
		return u.Status, true
	}
	return nil, false
}

func TestInsertUsesDatabaseDefaultsForSelectedColumns(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	users, err := rasql.TableOf[generatedDefaultUser](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Default: "generated_user_id()"},
			{Name: "email", Type: schema.TextType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	// id and status are omitted even though the generated row maps every
	// column. email is bound as an empty string, proving its zero value is not
	// mistaken for an absent value.
	mock.ExpectExec("INSERT INTO \"users\" (\"email\") VALUES ($1)").
		WithArgs("").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = rasql.InsertWithOptions(
		t.Context(),
		db,
		users,
		generatedDefaultUser{Email: ""},
		rasql.DefaultColumns("id", "status"),
	)
	require.NoError(t, err)
}

func TestInsertRejectsUnknownDefaultColumn(t *testing.T) {
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

	_, err = rasql.InsertWithOptions(t.Context(), buildOnlyDB(t), users, user{}, rasql.DefaultColumns("missing"))
	require.ErrorContains(t, err, "has no column \"missing\" selected for a database default")
}

// TestInsertRejectsTableFromWithInvalidDescriptor pins that the write path
// still revalidates a table built by rasql.TableFrom, which itself validates
// nothing: typedTableReference calls definition.Validate() on every write, so
// an invalid descriptor is rejected here even though it would render on the
// read path (query.TableRefFrom's contract).
func TestInsertRejectsTableFromWithInvalidDescriptor(t *testing.T) {
	bad := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{""},
	}
	type user struct {
		ID    int64  `rasql:""`
		Email string `rasql:"email"`
	}
	users := rasql.TableFrom[user](bad)

	_, err := rasql.Insert(t.Context(), buildOnlyDB(t), users, user{Email: "ada@example.com"})
	require.ErrorContains(t, err, "table reference")
}

func TestInsertWithOptionsUsesDefaultsForEveryColumn(t *testing.T) {
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
			{Name: "id", Type: schema.IntegerType{}, Default: "next_user_id()"},
			{Name: "email", Type: schema.TextType{}, Default: "'unknown'"},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" DEFAULT VALUES").
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = rasql.InsertWithOptions(t.Context(), db, users, user{}, rasql.DefaultColumns("id", "email"))
	require.NoError(t, err)
}

func TestQueryWriteAllDecodesReturnedRows(t *testing.T) {
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
	statement, err := query.NewUpdate(users, query.Set(id, query.Bind(int64(1))))
	require.NoError(t, err)
	statement, err = statement.AllowAll()
	require.NoError(t, err)
	statement, err = statement.WithReturning(id)
	require.NoError(t, err)
	mock.ExpectQuery("UPDATE \"users\" SET \"id\" = $1 RETURNING \"id\"").
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)).AddRow(int64(2)))

	type user struct {
		ID int64 `rasql:"id"`
	}
	rows, err := rasql.QueryWriteAll[user](t.Context(), db, statement)
	require.NoError(t, err)
	require.Equal(t, []user{{ID: 1}, {ID: 2}}, rows)
}

func TestQueryWriteOneDecodesReturnedRow(t *testing.T) {
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
	statement, err := query.NewInsert(users, []query.ColumnRef{email}, "ada@example.com")
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, email)
	require.NoError(t, err)
	mock.ExpectQuery("INSERT INTO \"users\" (\"email\") VALUES ($1) RETURNING \"id\", \"email\"").
		WithArgs("ada@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(1), "ada@example.com"))

	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	result, err := rasql.QueryWriteOne[user](t.Context(), db, statement)
	require.NoError(t, err)
	require.Equal(t, user{ID: 1, Email: "ada@example.com"}, result)
}

func TestQueryWriteOneRejectsZeroOrMultipleRows(t *testing.T) {
	type user struct {
		ID int64 `rasql:"id"`
	}

	t.Run("zero rows", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		statement := deleteReturningStatement(t)
		mock.ExpectQuery("DELETE FROM \"users\" RETURNING \"id\"").
			WillReturnRows(sqlmock.NewRows([]string{"id"}))

		// QueryWriteOne shares exactlyOne with TypedSelectBuilder.One, so an empty
		// RETURNING result reports the same sentinels.
		_, err = rasql.QueryWriteOne[user](t.Context(), db, statement)
		require.EqualError(t, err, "rasql: expected one row, got none")
		require.ErrorIs(t, err, rasql.ErrNoRows)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.NotErrorIs(t, err, rasql.ErrMultipleRows)
	})

	t.Run("multiple rows", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		statement := deleteReturningStatement(t)
		mock.ExpectQuery("DELETE FROM \"users\" RETURNING \"id\"").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)).AddRow(int64(2)))

		_, err = rasql.QueryWriteOne[user](t.Context(), db, statement)
		require.EqualError(t, err, "rasql: expected one row, got more than one")
		require.ErrorIs(t, err, rasql.ErrMultipleRows)
		require.NotErrorIs(t, err, rasql.ErrNoRows)
		require.NotErrorIs(t, err, sql.ErrNoRows)
	})
}

func TestQueryWriteOneReportsDecodeError(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := deleteReturningStatement(t)
	mock.ExpectQuery("DELETE FROM \"users\" RETURNING \"id\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))

	// email is not among the returning projections, so decoding a row type that
	// requires it fails the same way TypedSelectBuilder.Query does.
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	_, err = rasql.QueryWriteOne[user](t.Context(), db, statement)
	require.ErrorContains(t, err, "rasql: decode row 0: row: column \"email\" is not present")
}

// generatedReturningUser has the direct scan mapping rasqlgen writes for a row
// type. Every field is tagged out of the field-mapping fallback, because its
// field names would otherwise snake-case to "id" and "email" and match the
// projection, letting the fallback silently succeed even if the scan path
// broke. With every field tagged "-", a successful scan proves
// QueryWriteOne actually used ScanRow/ScanDestinations rather than falling
// back to field mapping.
type generatedReturningUser struct {
	ID    int64  `rasql:"-"`
	Email string `rasql:"-"`
}

func (u *generatedReturningUser) ScanColumns() []string {
	return []string{"id", "email"}
}

func (u *generatedReturningUser) ScanRow(source rasql.ScanSource) error {
	return source.Scan(&u.ID, &u.Email)
}

func (u *generatedReturningUser) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	for index, column := range columns {
		switch column {
		case "id":
			destinations[index] = &u.ID
		case "email":
			destinations[index] = &u.Email
		default:
			return nil, fmt.Errorf("unknown column %q", column)
		}
	}
	return destinations, nil
}

func TestQueryWriteOneScansGeneratedRowDirectly(t *testing.T) {
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
	statement, err := query.NewInsert(users, []query.ColumnRef{email}, "ada@example.com")
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, email)
	require.NoError(t, err)
	mock.ExpectQuery("INSERT INTO \"users\" (\"email\") VALUES ($1) RETURNING \"id\", \"email\"").
		WithArgs("ada@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(1), "ada@example.com"))

	result, err := rasql.QueryWriteOne[generatedReturningUser](t.Context(), db, statement)
	require.NoError(t, err)
	require.Equal(t, generatedReturningUser{ID: 1, Email: "ada@example.com"}, result)
}

func TestQueryWriteAllHonorsCompleteGeneratedReturning(t *testing.T) {
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
	statement, err := query.NewInsert(users, []query.ColumnRef{email}, "ada@example.com")
	require.NoError(t, err)
	statement, err = statement.WithReturning(id, email)
	require.NoError(t, err)
	mock.ExpectQuery("INSERT INTO \"users\" (\"email\") VALUES ($1) RETURNING \"id\", \"email\"").
		WithArgs("ada@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "ada@example.com").
			AddRow(int64(2), "bob@example.com"))

	rows, err := rasql.QueryWriteAll[generatedReturningUser](t.Context(), db, statement)
	require.NoError(t, err)
	require.Equal(t, []generatedReturningUser{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
	}, rows)
}

func TestQueryWriteRejectsIncompleteGeneratedReturning(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := deleteReturningStatement(t)

	_, err = rasql.QueryWriteOne[generatedReturningUser](t.Context(), db, statement)
	require.EqualError(t, err, `rasql: RETURNING projections omit generated row column "email"`)

	_, err = rasql.QueryWriteAll[generatedReturningUser](t.Context(), db, statement)
	require.EqualError(t, err, `rasql: RETURNING projections omit generated row column "email"`)
}

type partialReturningUser struct {
	ID    int64
	Email string
}

func (u *partialReturningUser) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	var discard any
	for index, column := range columns {
		if column == "id" {
			destinations[index] = &u.ID
			continue
		}
		destinations[index] = &discard
	}
	return destinations, nil
}

func TestQueryWriteAllowsIncompleteCustomReturning(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	statement := deleteReturningStatement(t)
	mock.ExpectQuery("DELETE FROM \"users\" RETURNING \"id\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))

	result, err := rasql.QueryWriteOne[partialReturningUser](t.Context(), db, statement)
	require.NoError(t, err)
	require.Equal(t, partialReturningUser{ID: 1}, result)
}

// deleteReturningStatement builds a DELETE that returns id, which the
// QueryWriteOne error-path tests share.
func deleteReturningStatement(t *testing.T) query.Delete {
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
	id := users.Column("id")
	statement, err := query.NewDelete(users)
	require.NoError(t, err)
	statement, err = statement.AllowAll()
	require.NoError(t, err)
	statement, err = statement.WithReturning(id)
	require.NoError(t, err)
	return statement
}

func TestUpdateExecutesTypedRow(t *testing.T) {
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
	mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
		WithArgs("grace@example.com", int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := rasql.Update(t.Context(), db, users, user{ID: 42, Email: "grace@example.com"})
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

func TestUpdateWithOptionsUpdatesSelectedFieldsByPrimaryKey(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type patch struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[patch](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	mock.ExpectExec(`UPDATE "users" SET "email" = $1 WHERE ("users"."id" = $2)`).
		WithArgs("grace@example.com", int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.UpdateWithOptions(
		t.Context(),
		db,
		users,
		patch{ID: 42, Email: "grace@example.com"},
		rasql.UpdateColumns("email"),
	)
	require.NoError(t, err)
}

func TestUpdateManyBulkUpdatesPartialRowByPredicate(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type patch struct {
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[patch](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")
	mock.ExpectExec(`UPDATE "users" SET "email" = $1 WHERE ("users"."id" IN ($2, $3))`).
		WithArgs("review@example.com", int64(1), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	_, err = rasql.UpdateMany(
		t.Context(),
		db,
		users,
		patch{Email: "review@example.com"},
		rasql.UpdateColumns("email"),
		rasql.UpdateWhere(query.In(id, query.Bind(int64(1)), query.Bind(int64(2)))),
	)
	require.NoError(t, err)
}

func TestUpdateWithOptionsRejectsInvalidConfiguration(t *testing.T) {
	type patch struct {
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[patch](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	_, err = rasql.UpdateWithOptions(t.Context(), buildOnlyDB(t), users, patch{}, rasql.UpdateColumns("missing"))
	require.ErrorContains(t, err, `has no column "missing" selected for update`)
	_, err = rasql.UpdateWithOptions(t.Context(), buildOnlyDB(t), users, patch{}, rasql.UpdateWhere(nil))
	require.ErrorContains(t, err, "update predicate must not be nil")
	_, err = rasql.UpdateWithOptions(t.Context(), buildOnlyDB(t), users, patch{Email: "x"}, rasql.UpdateColumns("email"))
	require.ErrorContains(t, err, `row value has no field tagged for column "id"`)
	_, err = rasql.UpdateMany(t.Context(), buildOnlyDB(t), users, patch{Email: "x"}, rasql.UpdateColumns("email"))
	require.ErrorContains(t, err, "bulk update requires an explicit UpdateWhere predicate")
}

func TestUpdateRejectsTableWithoutPrimaryKey(t *testing.T) {
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
	}
	users, err := rasql.TableOf[user](table)
	require.NoError(t, err)

	_, err = rasql.Update(t.Context(), buildOnlyDB(t), users, user{ID: 42, Email: "grace@example.com"})
	require.ErrorContains(t, err, "has no primary key")
}

// TestTypedWriteNamesQualifiedTableInErrors pins the three typed-write
// messages that name a table. Each one used to print the bare name, so a
// qualified table reported "users" and told the caller nothing about which
// schema the table it complained about sits in.
func TestTypedWriteNamesQualifiedTableInErrors(t *testing.T) {
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	type key struct {
		ID int64 `rasql:"id"`
	}
	columns := []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}},
	}

	keyed, err := rasql.TableOf[user](schema.TableDef{
		Schema:     "tenant",
		Name:       "users",
		Columns:    columns,
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	_, err = rasql.InsertWithOptions(t.Context(), buildOnlyDB(t), keyed, user{}, rasql.DefaultColumns("missing"))
	require.ErrorContains(t, err, `table "tenant.users" has no column "missing" selected for a database default`)

	unkeyed, err := rasql.TableOf[user](schema.TableDef{
		Schema:  "tenant",
		Name:    "users",
		Columns: columns,
	})
	require.NoError(t, err)
	_, err = rasql.Update(t.Context(), buildOnlyDB(t), unkeyed, user{ID: 42})
	require.ErrorContains(t, err, `table "tenant.users" has no primary key`)

	allKey, err := rasql.TableOf[key](schema.TableDef{
		Schema:     "tenant",
		Name:       "users",
		Columns:    columns[:1],
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	_, err = rasql.Update(t.Context(), buildOnlyDB(t), allKey, key{ID: 42})
	require.ErrorContains(t, err, `table "tenant.users" has no non-primary-key columns`)
}

func TestUpdateMatchesCompositePrimaryKey(t *testing.T) {
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
		Name: "memberships",
		Columns: []schema.ColumnDef{
			{Name: "account_id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "role", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"account_id", "user_id"},
	}
	type membership struct {
		AccountID int64  `rasql:"account_id"`
		UserID    int64  `rasql:"user_id"`
		Role      string `rasql:"role"`
	}
	memberships, err := rasql.TableOf[membership](table)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE \"memberships\" SET \"role\" = $1 WHERE ((\"memberships\".\"account_id\" = $2) AND (\"memberships\".\"user_id\" = $3))").
		WithArgs("admin", int64(10), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), db, memberships, membership{AccountID: 10, UserID: 42, Role: "admin"})
	require.NoError(t, err)
}

// mappedUser supplies its own column values and carries a tag naming a
// different column, so a write through the tag is distinguishable from one
// through the method.
type mappedUser struct {
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u mappedUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// wrappedUser embeds a row type that supplies its own column values and tags no
// field of its own, so only the promoted ColumnValue can map it.
type wrappedUser struct {
	mappedUser
}

// taggedWrapperUser embeds the same row type and tags fields of its own, so the
// promoted ColumnValue and the tags name different values for every column.
type taggedWrapperUser struct {
	mappedUser
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// declaringWrapperUser embeds the same row type, tags fields of its own, and
// declares ColumnValue as well. Its tags are crossed and its embedded fields
// hold values of their own, so the embedded mapping, the tag mapping, and the
// declared method each name a different value for every column. Go dispatches to
// the declared method, so the writes must follow it.
type declaringWrapperUser struct {
	mappedUser
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u declaringWrapperUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// pointerDeclaringWrapperUser is the same shape with a pointer receiver, which
// is the other place a row type can declare its mapping method.
type pointerDeclaringWrapperUser struct {
	mappedUser
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u *pointerDeclaringWrapperUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// pointerMappedUser supplies its own column values through a pointer receiver
// and embeds nothing, so its value type carries no ColumnValue at all.
type pointerMappedUser struct {
	ID    int64
	Email string
}

func (u *pointerMappedUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// pointerReceiverWrapperUser embeds a row type whose ColumnValue has a pointer
// receiver, so the promoted method reaches the outer type only through its
// pointer. That is the same method set a row type takes when it declares
// ColumnValue with a pointer receiver of its own.
type pointerReceiverWrapperUser struct {
	pointerMappedUser
}

// taggedPointerReceiverWrapperUser is that shape with tags of its own.
type taggedPointerReceiverWrapperUser struct {
	pointerMappedUser
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// pointerWrappedUser embeds a pointer to a row type that supplies its own column
// values and tags no field of its own, so only the promoted ColumnValue can map
// it.
type pointerWrappedUser struct {
	*mappedUser
}

// taggedPointerWrapperUser embeds the same pointer and tags fields of its own, so
// the promoted ColumnValue and the tags name different values for every column.
type taggedPointerWrapperUser struct {
	*mappedUser
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// declaringPointerWrapperUser embeds the same pointer, tags fields of its own,
// and declares ColumnValue with a pointer receiver, so the embedded mapping, the
// tag mapping, and the declared method each name a different value.
type declaringPointerWrapperUser struct {
	*mappedUser
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u *declaringPointerWrapperUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// interfaceWrappedUser embeds the interface itself rather than a row type, which
// is the third way Go promotes ColumnValue to an outer struct.
type interfaceWrappedUser struct {
	rasql.ColumnValuer
}

// taggedInterfaceWrapperUser embeds the interface and tags fields of its own.
type taggedInterfaceWrapperUser struct {
	rasql.ColumnValuer
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// declaringInterfaceWrapperUser embeds the interface, tags fields of its own, and
// declares ColumnValue with a value receiver.
type declaringInterfaceWrapperUser struct {
	rasql.ColumnValuer
	ID    int64  `rasql:"email"`
	Email string `rasql:"id"`
}

func (u declaringInterfaceWrapperUser) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return u.ID, true
	case "email":
		return u.Email, true
	}
	return nil, false
}

// partiallyMappedUser omits one column of its table.
type partiallyMappedUser struct {
	ID int64
}

func (u partiallyMappedUser) ColumnValue(name string) (any, bool) {
	if name == "id" {
		return u.ID, true
	}
	return nil, false
}

func TestWritesUseColumnValuer(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}

	t.Run("insert", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[mappedUser](table)
		require.NoError(t, err)
		// The tags are crossed, so these arguments prove ColumnValue supplied them.
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "ada@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), db, users, mappedUser{ID: 42, Email: "ada@example.com"})
		require.NoError(t, err)
	})

	t.Run("update", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[mappedUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("grace@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), db, users, mappedUser{ID: 42, Email: "grace@example.com"})
		require.NoError(t, err)
	})

	t.Run("embedded without tags of its own", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[wrappedUser](table)
		require.NoError(t, err)
		// The wrapper tags nothing, so these arguments prove the promoted
		// ColumnValue supplied them rather than the tag path failing.
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "ada@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), db, users, wrappedUser{mappedUser{ID: 42, Email: "ada@example.com"}})
		require.NoError(t, err)
	})

	t.Run("missing column", func(t *testing.T) {
		users, err := rasql.TableOf[partiallyMappedUser](table)
		require.NoError(t, err)

		_, err = rasql.Insert(t.Context(), buildOnlyDB(t), users, partiallyMappedUser{ID: 42})
		require.ErrorContains(t, err, "supplies no value for column \"email\"")
	})
}

func TestWritesIgnorePromotedColumnValuer(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	// The embedded values differ from the outer tagged ones, so the arguments
	// below name which mapping ran.
	value := taggedWrapperUser{
		mappedUser: mappedUser{ID: 7, Email: "embedded@example.com"},
		ID:         42,
		Email:      "ada@example.com",
	}

	t.Run("insert", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[taggedWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "ada@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), db, users, value)
		require.NoError(t, err)
	})

	t.Run("update", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[taggedWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("ada@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), db, users, value)
		require.NoError(t, err)
	})
}

func TestWritesFollowDeclaredColumnValuer(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	// The embedded values, the crossed tags, and the declared method each name a
	// different value for both columns, so the arguments below name which mapping
	// ran: the declared method binds 42 and method@example.com, the tags bind
	// method@example.com and 42, and the promoted method binds 7 and
	// embedded@example.com.
	value := declaringWrapperUser{
		mappedUser: mappedUser{ID: 7, Email: "embedded@example.com"},
		ID:         42,
		Email:      "method@example.com",
	}
	pointerValue := pointerDeclaringWrapperUser{
		mappedUser: mappedUser{ID: 7, Email: "embedded@example.com"},
		ID:         42,
		Email:      "method@example.com",
	}

	t.Run("go dispatches to the declared method", func(t *testing.T) {
		columnValue, ok := value.ColumnValue("id")
		require.True(t, ok)
		require.Equal(t, int64(42), columnValue)
		columnValue, ok = pointerValue.ColumnValue("email")
		require.True(t, ok)
		require.Equal(t, "method@example.com", columnValue)
	})

	t.Run("insert", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[declaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "method@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), db, users, value)
		require.NoError(t, err)
	})

	t.Run("update", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[declaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("method@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), db, users, value)
		require.NoError(t, err)
	})

	t.Run("insert with a pointer receiver", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[pointerDeclaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
			WithArgs(int64(42), "method@example.com").
			WillReturnResult(sqlmock.NewResult(1, 1))

		_, err = rasql.Insert(t.Context(), db, users, pointerValue)
		require.NoError(t, err)
	})

	t.Run("update with a pointer receiver", func(t *testing.T) {
		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		users, err := rasql.TableOf[pointerDeclaringWrapperUser](table)
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
			WithArgs("method@example.com", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		_, err = rasql.Update(t.Context(), db, users, pointerValue)
		require.NoError(t, err)
	})
}

// usersTable is the table the mapping fixtures above are written against.
func usersTable() schema.TableDef {
	return schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

// requireInsertBinds requires that inserting value binds id and email, which
// names which of the candidate mappings the write side followed.
func requireInsertBinds[T any](t *testing.T, value T, id int64, email string) {
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
	users, err := rasql.TableOf[T](usersTable())
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").
		WithArgs(id, email).
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = rasql.Insert(t.Context(), db, users, value)
	require.NoError(t, err)
}

// requireUpdateBinds is requireInsertBinds for the other write.
func requireUpdateBinds[T any](t *testing.T, value T, id int64, email string) {
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
	users, err := rasql.TableOf[T](usersTable())
	require.NoError(t, err)
	mock.ExpectExec("UPDATE \"users\" SET \"email\" = $1 WHERE (\"users\".\"id\" = $2)").
		WithArgs(email, id).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), db, users, value)
	require.NoError(t, err)
}

// TestWritesRouteEveryEmbeddingShape covers the shapes an embedded ColumnValuer
// can take beyond the embedded value field the tests above use: an embedded
// pointer, an embedded interface, and a row type whose only ColumnValue has a
// pointer receiver. Each shape is written three ways -- without tags, with tags,
// and with tags plus a declared ColumnValue -- because the write side routes the
// three differently.
func TestWritesRouteEveryEmbeddingShape(t *testing.T) {
	t.Run("pointer receiver without embedding", func(t *testing.T) {
		// The row type declares its mapping, so the method binds these values
		// even though no field of it is tagged.
		value := pointerMappedUser{ID: 42, Email: "ada@example.com"}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded pointer-receiver method without tags of its own", func(t *testing.T) {
		// The promoted method reaches this row type only through its pointer,
		// which is where Insert and Update look for it.
		value := pointerReceiverWrapperUser{pointerMappedUser{ID: 42, Email: "ada@example.com"}}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded pointer-receiver method with tags of its own", func(t *testing.T) {
		value := taggedPointerReceiverWrapperUser{
			pointerMappedUser: pointerMappedUser{ID: 7, Email: "embedded@example.com"},
			ID:                42,
			Email:             "ada@example.com",
		}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded pointer without tags of its own", func(t *testing.T) {
		// The wrapper tags nothing, so the promoted ColumnValue maps it.
		value := pointerWrappedUser{&mappedUser{ID: 42, Email: "ada@example.com"}}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded pointer with tags of its own", func(t *testing.T) {
		// The embedded values differ from the outer tagged ones, so binding the
		// outer ones proves the tags mapped it rather than the promoted method.
		value := taggedPointerWrapperUser{
			mappedUser: &mappedUser{ID: 7, Email: "embedded@example.com"},
			ID:         42,
			Email:      "ada@example.com",
		}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded pointer with a declared method", func(t *testing.T) {
		// The declared method binds 42 and method@example.com, the crossed tags
		// would bind method@example.com and 42, and the promoted method would
		// bind 7 and embedded@example.com.
		value := declaringPointerWrapperUser{
			mappedUser: &mappedUser{ID: 7, Email: "embedded@example.com"},
			ID:         42,
			Email:      "method@example.com",
		}
		requireInsertBinds(t, value, 42, "method@example.com")
		requireUpdateBinds(t, value, 42, "method@example.com")
	})

	t.Run("embedded interface without tags of its own", func(t *testing.T) {
		value := interfaceWrappedUser{mappedUser{ID: 42, Email: "ada@example.com"}}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded interface with tags of its own", func(t *testing.T) {
		value := taggedInterfaceWrapperUser{
			ColumnValuer: mappedUser{ID: 7, Email: "embedded@example.com"},
			ID:           42,
			Email:        "ada@example.com",
		}
		requireInsertBinds(t, value, 42, "ada@example.com")
		requireUpdateBinds(t, value, 42, "ada@example.com")
	})

	t.Run("embedded interface with a declared method", func(t *testing.T) {
		value := declaringInterfaceWrapperUser{
			ColumnValuer: mappedUser{ID: 7, Email: "embedded@example.com"},
			ID:           42,
			Email:        "method@example.com",
		}
		requireInsertBinds(t, value, 42, "method@example.com")
		requireUpdateBinds(t, value, 42, "method@example.com")
	})
}

const (
	// autogeneratedFile is the file name the Go toolchain reports for a method it
	// generates rather than one a package declares.
	autogeneratedFile = "<autogenerated>"
	// originPremise states what the write side reads that file name for, so a Go
	// release that reports another name is told what it broke.
	originPremise = "rasql tells a ColumnValue a row type declares from one Go promotes out of an embedded field by the file this build reports for the method, and nothing else reflect can see separates the two. Insert and Update route a row type that embeds a ColumnValuer by that answer, so a build that reports another name binds the wrong values for such a row type."

	originNone      = "none"
	originGenerated = "generated"
	originDeclared  = "declared"
)

// TestGeneratedMethodsReportGeneratedFile states the toolchain premise the write
// side rests on, for every shape it routes by. It fails on a Go release that
// reports a promoted method as declared, or a declared one as generated, rather
// than leaving that release to mis-bind a write.
func TestGeneratedMethodsReportGeneratedFile(t *testing.T) {
	// Every fixture named here is declared in this file, so a declared
	// ColumnValue must be reported as coming from it.
	const declaringFile = "typed_write_test.go"

	for _, testCase := range []struct {
		name          string
		rowType       reflect.Type
		valueOrigin   string
		pointerOrigin string
	}{
		{
			name:          "value receiver",
			rowType:       reflect.TypeFor[mappedUser](),
			valueOrigin:   originDeclared,
			pointerOrigin: originGenerated,
		},
		{
			name:          "pointer receiver",
			rowType:       reflect.TypeFor[pointerMappedUser](),
			valueOrigin:   originNone,
			pointerOrigin: originDeclared,
		},
		{
			name:          "embedded value",
			rowType:       reflect.TypeFor[wrappedUser](),
			valueOrigin:   originGenerated,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded value with tags",
			rowType:       reflect.TypeFor[taggedWrapperUser](),
			valueOrigin:   originGenerated,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded value with a value-receiver method",
			rowType:       reflect.TypeFor[declaringWrapperUser](),
			valueOrigin:   originDeclared,
			pointerOrigin: originGenerated,
		},
		{
			// The declared method shadows the promoted one, and a pointer
			// receiver keeps it out of the value type's method set, so the value
			// type carries no ColumnValue at all.
			name:          "embedded value with a pointer-receiver method",
			rowType:       reflect.TypeFor[pointerDeclaringWrapperUser](),
			valueOrigin:   originNone,
			pointerOrigin: originDeclared,
		},
		{
			// This is the shape above with the method promoted rather than
			// declared. The two are the same method set, and the file name is
			// all that separates them.
			name:          "embedded pointer-receiver method",
			rowType:       reflect.TypeFor[pointerReceiverWrapperUser](),
			valueOrigin:   originNone,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded pointer-receiver method with tags",
			rowType:       reflect.TypeFor[taggedPointerReceiverWrapperUser](),
			valueOrigin:   originNone,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded pointer",
			rowType:       reflect.TypeFor[pointerWrappedUser](),
			valueOrigin:   originGenerated,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded pointer with tags",
			rowType:       reflect.TypeFor[taggedPointerWrapperUser](),
			valueOrigin:   originGenerated,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded pointer with a pointer-receiver method",
			rowType:       reflect.TypeFor[declaringPointerWrapperUser](),
			valueOrigin:   originNone,
			pointerOrigin: originDeclared,
		},
		{
			name:          "embedded interface",
			rowType:       reflect.TypeFor[interfaceWrappedUser](),
			valueOrigin:   originGenerated,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded interface with tags",
			rowType:       reflect.TypeFor[taggedInterfaceWrapperUser](),
			valueOrigin:   originGenerated,
			pointerOrigin: originGenerated,
		},
		{
			name:          "embedded interface with a value-receiver method",
			rowType:       reflect.TypeFor[declaringInterfaceWrapperUser](),
			valueOrigin:   originDeclared,
			pointerOrigin: originGenerated,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			requireMethodOrigin(t, testCase.rowType, testCase.valueOrigin, declaringFile)
			requireMethodOrigin(t, reflect.PointerTo(testCase.rowType), testCase.pointerOrigin, declaringFile)
		})
	}
}

// requireMethodOrigin requires that this Go build reports methodType's
// ColumnValue as want.
func requireMethodOrigin(t *testing.T, methodType reflect.Type, want string, declaringFile string) {
	t.Helper()

	method, ok := methodType.MethodByName("ColumnValue")
	if want == originNone {
		require.Falsef(t, ok, "%s carries no ColumnValue in its method set, and this Go build reports one. %s", methodType, originPremise)
		return
	}
	require.Truef(t, ok, "%s carries a ColumnValue in its method set, and this Go build reports none. %s", methodType, originPremise)

	pointer := method.Func.Pointer()
	function := runtime.FuncForPC(pointer)
	require.NotNilf(t, function, "this Go build reports no function at all for %s.ColumnValue. %s", methodType, originPremise)
	file, _ := function.FileLine(pointer)

	if want == originGenerated {
		require.Equalf(t, autogeneratedFile, file, "the Go toolchain generates %s.ColumnValue, and this build reports its file as %q rather than %q. %s", methodType, file, autogeneratedFile, originPremise)
		return
	}
	require.Truef(t, strings.HasSuffix(file, declaringFile), "%s.ColumnValue is declared in %s, and this Go build reports its file as %q. %s", methodType, declaringFile, file, originPremise)
}

func TestInsertRejectsMissingTaggedColumn(t *testing.T) {
	type user struct {
		ID int64 `rasql:"id"`
	}
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	users, err := rasql.TableOf[user](table)
	require.NoError(t, err)

	_, err = rasql.Insert(t.Context(), buildOnlyDB(t), users, user{ID: 42})
	require.ErrorContains(t, err, "no field tagged for column \"email\"")
}
