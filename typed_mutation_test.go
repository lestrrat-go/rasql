package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type usersRowDecoder struct{ schema rasql.ResultSchema }

func (d usersRowDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (usersRowDecoder) Presence() []rasql.Presence         { return nil }
func (d usersRowDecoder) DecodeRow(source rasql.ScanSource, row *store.UsersRow) error {
	return source.Scan(&row.ID, &row.Email, &row.Nickname, &row.Status, &row.FirstName, &row.LastName)
}

// usersRowProjection reads store.UsersRow back through the same table
// store.Users() names, binding each of its own columns rather than a second,
// hand-declared one -- DynamicProjection does not fit here: it decodes an
// existing result set, but a RETURNING projection needs a real expression
// for every column it names, and DynamicProjection's items are that column's
// name projecting a bound NULL, not the column itself.
func usersRowProjection(t *testing.T) rasql.Projection[store.UsersRow] {
	t.Helper()

	relation, err := rasql.SourceOf[store.UsersRow](store.Users(), "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[store.UsersRow, int64](relation, "id", "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[store.UsersRow, string](relation, "email", "")
	require.NoError(t, err)
	nickname, err := rasql.BindNullColumn[store.UsersRow, string](relation, "nickname", "")
	require.NoError(t, err)
	status, err := rasql.BindColumn[store.UsersRow, string](relation, "status", "")
	require.NoError(t, err)
	firstName, err := rasql.BindColumn[store.UsersRow, string](relation, "first_name", "")
	require.NoError(t, err)
	lastName, err := rasql.BindColumn[store.UsersRow, string](relation, "last_name", "")
	require.NoError(t, err)

	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		rasql.ResultColumn{Name: "status", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "first_name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "last_name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", email.Expr(), schema.TextType{}, ""),
		rasql.NullItem("nickname", nickname.NullExpr(), schema.TextType{}, ""),
		rasql.Item("status", status.Expr(), schema.TextType{}, ""),
		rasql.Item("first_name", firstName.Expr(), schema.TextType{}, ""),
		rasql.Item("last_name", lastName.Expr(), schema.TextType{}, ""),
	}, usersRowDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func TestGeneratedMutationPlansPersistDefaultsAndPresence(t *testing.T) {
	database, err := sql.Open("sqlite", "file:typed_mutation?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		nickname TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		first_name TEXT NOT NULL,
		last_name TEXT NOT NULL
	)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	projection := usersRowProjection(t)

	createQuery, err := rasql.Returning(store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan(), projection)
	require.NoError(t, err)
	created, err := rasql.One(t.Context(), executor, createQuery)
	require.NoError(t, err)
	require.Equal(t, int64(1), created.ID)
	require.Equal(t, "pending", created.Status)
	patch, err := store.NewUsersPatch().Status("active").Where(queryEqualID(created.ID))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, patch)
	require.NoError(t, err)
	patch, err = store.NewUsersPatch().ClearNickname().Where(queryEqualID(created.ID))
	require.NoError(t, err)
	patchQuery, err := rasql.Returning(patch, projection)
	require.NoError(t, err)
	updated, err := rasql.One(t.Context(), executor, patchQuery)
	require.NoError(t, err)
	require.Equal(t, "active", updated.Status)
	require.Nil(t, updated.Nickname)
}

func queryEqualID(id int64) query.Predicate {
	return query.EqualValue(store.Users().ID(), id)
}
