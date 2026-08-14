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

// TestPostgreSQLInspectorRecordsGINIndexMethodAgainstLiveDatabase pins what a
// real PostgreSQL server reports back for a GIN index, because
// TestPostgreSQLInspectorRecordsNonDefaultIndexMethod in inspect_test.go only
// asserts rasql's own mocked catalog echoes what the test told it to. Before
// this feature existed, inspecting this table failed outright: the GIN index
// made the whole table unrepresentable, which is the exact failure a sweep
// over a production schema must not hit on its first non-btree index.
func TestPostgreSQLInspectorRecordsGINIndexMethodAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, tags text[] NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_tags_gin_idx ON articles USING GIN (tags)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_tags_gin_idx", Columns: []string{"tags"}, Method: "gin"},
	}, table.Indexes)
}

// TestMySQLInspectorRecordsFullTextIndexMethodAgainstLiveDatabase is the
// MySQL counterpart: a FULLTEXT index used to make the whole table
// unrepresentable, and this pins that a real server's
// information_schema.statistics.index_type of "FULLTEXT" is what
// schema.IndexDef.Method now records for it.
func TestMySQLInspectorRecordsFullTextIndexMethodAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id INT PRIMARY KEY, body TEXT NOT NULL) ENGINE=InnoDB")
	mustExec(t, ctx, database, "CREATE FULLTEXT INDEX articles_body_fulltext_idx ON articles (body)")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_body_fulltext_idx", Columns: []string{"body"}, Method: "FULLTEXT"},
	}, table.Indexes)
}
