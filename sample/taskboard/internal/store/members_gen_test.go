package store

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestMembersRowScansSQLRowDirectly(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	var member MembersRow
	err = member.ScanRow(database.QueryRowContext(t.Context(), "SELECT 7, 'Ada Lovelace', 'ada@example.com'"))
	require.NoError(t, err)
	require.Equal(t, MembersRow{ID: 7, Name: "Ada Lovelace", Email: "ada@example.com"}, member)
}

func TestMembersRowScansPartialTypedResult(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	_, err = database.ExecContext(t.Context(), `
		CREATE TABLE members (
			id INTEGER NOT NULL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL
		);
		INSERT INTO members (id, name, email) VALUES (7, 'Ada Lovelace', 'ada@example.com');
	`)
	require.NoError(t, err)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	members := Members()
	member, err := rasql.DecodeFrom[MembersRow](members).
		Project(query.Project(members.Name())).
		One(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, MembersRow{Name: "Ada Lovelace"}, member)
}
