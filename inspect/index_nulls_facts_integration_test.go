//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestPostgreSQLInspectorRecordsIndexNullsNotDistinctAgainstLiveDatabase
// pins what a real PostgreSQL server reports back for a plain
// (non-constraint) unique index declared NULLS NOT DISTINCT, because the
// sqlmock-based TestPostgreSQLInspectorRecordsIndexNullsFacts in
// inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to. Before this feature existed, inspecting this table
// failed outright: the NULLS NOT DISTINCT index made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema must not hit on its first index carrying one.
func TestPostgreSQLInspectorRecordsIndexNullsNotDistinctAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, code text)")
	mustExec(t, ctx, database, "CREATE UNIQUE INDEX articles_code_uidx ON articles (code) NULLS NOT DISTINCT")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_code_uidx", Columns: []string{"code"}, Unique: true, NullsNotDistinct: true},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsIndexNullsOrderAgainstLiveDatabase pins
// what a real PostgreSQL server reports back for an index key's NULLS
// FIRST placement, which overrides the NULLS LAST default an ascending key
// otherwise implies. Before this feature existed, inspecting this table
// failed outright: the nondefault nulls placement made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema must not hit on its first such index.
func TestPostgreSQLInspectorRecordsIndexNullsOrderAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, code text)")
	mustExec(t, ctx, database, "CREATE INDEX articles_code_idx ON articles (code NULLS FIRST)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_code_idx", Keys: []schema.IndexKeyDef{{Expression: "code", NullsOrder: schema.NullsFirst}}},
	}, table.Indexes)
}
