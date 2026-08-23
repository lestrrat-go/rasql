package dynamic_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// dynamicDeleteUser gives typedUsersTable a Go row type, so the same table
// shape can be built both as a query.TableRef for dynamic.DeleteFrom and as a
// rasql.Table[T] for rasql.DeleteFrom, which TestDeleteFromMatchesTypedDelete
// compares against each other.
type dynamicDeleteUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

// typedUsersTable returns the same table shape dynamicUsersTable does, as a
// typed rasql.Table so a test can build the same statement through
// rasql.DeleteFrom and dynamic.DeleteFrom and compare the rendered SQL.
func typedUsersTable(t *testing.T) rasql.Table[dynamicDeleteUser] {
	t.Helper()

	users, err := rasql.TableOf[dynamicDeleteUser](schema.TableDef{
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
	users := dynamicUsersTable(t)
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
			_, err := dynamic.DeleteFrom(users).Where(predicate.expression).Build(d)
			require.ErrorContains(t, err, "WHERE expression must not be nil")
		})
	}
}

// TestDeleteFromMatchesTypedDelete proves dynamic.DeleteFrom and
// rasql.DeleteFrom render identical SQL for the same table and predicate, the
// proof the two builders did not drift apart when dynamic.DeleteBuilder was
// copied out of root rather than shared with it.
func TestDeleteFromMatchesTypedDelete(t *testing.T) {
	users := typedUsersTable(t)
	id := users.Column("id")
	d := dbForBuild(t).Dialect()

	fromDynamic, err := dynamic.DeleteFrom(users.Ref()).WhereEqual(id, 42).Build(d)
	require.NoError(t, err)
	fromTyped, err := rasql.DeleteFrom(users).WhereEqual(id, 42).Build(d)
	require.NoError(t, err)
	require.Equal(t, fromTyped.SQL(), fromDynamic.SQL())

	fromDynamicAllowAll, err := dynamic.DeleteFrom(users.Ref()).AllowAll().Build(d)
	require.NoError(t, err)
	fromTypedAllowAll, err := rasql.DeleteFrom(users).AllowAll().Build(d)
	require.NoError(t, err)
	require.Equal(t, fromTypedAllowAll.SQL(), fromDynamicAllowAll.SQL())
}

func TestDeleteReturningQuery(t *testing.T) {
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
	id := users.Column("id")
	email := users.Column("email")
	mock.ExpectQuery("DELETE FROM \"users\" WHERE (\"users\".\"id\" = $1) RETURNING \"id\", \"email\"").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(int64(42), "ada@example.com"))

	sequence, err := dynamic.DeleteFrom(users).
		WhereEqual(id, 42).
		Returning(id, email).
		Query(t.Context(), db)
	rows := collectRows(t, sequence, err)
	require.Len(t, rows, 1)
	gotID, err := dynamic.Get[int64](rows[0], "id")
	require.NoError(t, err)
	gotEmail, err := dynamic.Get[string](rows[0], "email")
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	require.Equal(t, "ada@example.com", gotEmail)
}

func TestDeleteReturningRequiresProjection(t *testing.T) {
	users := dynamicUsersTable(t)
	id := users.Column("id")

	_, err := dynamic.DeleteFrom(users).WhereEqual(id, 42).Returning().Build(dialect.PostgreSQL())
	require.EqualError(t, err, "rasql: RETURNING requires at least one projection")
}

func TestDeleteReturningRejectsUnsupportedDialect(t *testing.T) {
	users := dynamicUsersTable(t)
	id := users.Column("id")

	_, err := dynamic.DeleteFrom(users).
		WhereEqual(id, 42).
		Returning(id).
		Build(dialect.MySQL())
	require.ErrorContains(t, err, "RETURNING is not supported")
}
