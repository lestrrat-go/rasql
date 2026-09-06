package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteConditionalUpsertPreservesNewerRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, payload TEXT NOT NULL)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	table := query.MustTableRef(schema.MustTableDef("items", schema.Integer("id"), schema.Integer("version"), schema.Text("payload")))
	id, version, payload := table.Column("id"), table.Column("version"), table.Column("payload")
	seed, err := query.NewInsert(table, query.Set(id, 1), query.Set(version, 2), query.Set(payload, "v2"))
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, seed)
	require.NoError(t, err)

	upsert := func(versionValue int, payloadValue string) {
		insert, buildErr := query.NewInsert(table, query.Set(id, 1), query.Set(version, versionValue), query.Set(payload, payloadValue))
		require.NoError(t, buildErr)
		statement, buildErr := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
			query.Set(version, query.Excluded(version)), query.Set(payload, query.Excluded(payload)),
		})
		require.NoError(t, buildErr)
		statement, buildErr = statement.WithUpdateWhere(query.LessThan(version, query.Excluded(version)))
		require.NoError(t, buildErr)
		_, execErr := rasql.Exec(t.Context(), db, statement)
		require.NoError(t, execErr)
	}
	upsert(1, "v1")
	var storedVersion int
	var storedPayload string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT version, payload FROM items WHERE id = 1`).Scan(&storedVersion, &storedPayload))
	require.Equal(t, 2, storedVersion)
	require.Equal(t, "v2", storedPayload)
	upsert(3, "v3")
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT version, payload FROM items WHERE id = 1`).Scan(&storedVersion, &storedPayload))
	require.Equal(t, 3, storedVersion)
	require.Equal(t, "v3", storedPayload)
}
