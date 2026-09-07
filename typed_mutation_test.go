package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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
	created, err := rasql.QueryCreate(t.Context(), db, store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan())
	require.NoError(t, err)
	require.Equal(t, int64(1), created.ID)
	require.Equal(t, "pending", created.Status)
	patch, err := store.NewUsersPatch().Status("active").Where(queryEqualID(created.ID))
	require.NoError(t, err)
	_, err = rasql.ExecPatch(t.Context(), db, patch)
	require.NoError(t, err)
	patch, err = store.NewUsersPatch().ClearNickname().Where(queryEqualID(created.ID))
	require.NoError(t, err)
	updated, err := rasql.QueryPatchOne(t.Context(), db, patch)
	require.NoError(t, err)
	require.Equal(t, "active", updated.Status)
	require.Nil(t, updated.Nickname)
}

func queryEqualID(id int64) query.Predicate {
	return query.EqualValue(store.Users().ID(), id)
}
