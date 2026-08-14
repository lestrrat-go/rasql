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

// TestPostgreSQLInspectorRecordsPartialIndexAgainstLiveDatabase pins what a
// real PostgreSQL server reports back for a partial index's predicate,
// because TestPostgreSQLInspectorRecordsPartialIndex in inspect_test.go
// only asserts rasql's own mocked catalog echoes what the test told it to.
// Before this feature existed, inspecting this table failed outright: the
// partial index made the whole table unrepresentable, which is the exact
// failure a sweep over a production schema must not hit on its first
// partial index. pg_catalog.pg_get_expr is what actually reconstructs the
// predicate text from the index's internal representation, so this test is
// the only proof that route produces the SQL rasql expects rather than
// something else PostgreSQL considers equivalent.
func TestPostgreSQLInspectorRecordsPartialIndexAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	// The predicate is a bare boolean column reference rather than a
	// literal comparison, so pg_get_expr has nothing to add an explicit
	// type cast around: what it reports back is exactly "published",
	// letting this test assert an exact string instead of a substring.
	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, published boolean NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_published_idx ON articles (id) WHERE published")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_published_idx", Columns: []string{"id"}, Predicate: "published"},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsExpressionIndexAgainstLiveDatabase pins what
// a real PostgreSQL server reports back for an expression index's key text
// through pg_catalog.pg_get_indexdef, the same way
// TestPostgreSQLInspectorRecordsPartialIndexAgainstLiveDatabase pins
// pg_get_expr for a predicate: TestPostgreSQLInspectorRecordsExpressionIndex
// in inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to.
func TestPostgreSQLInspectorRecordsExpressionIndexAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, title text NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_lower_title_idx ON articles (lower(title))")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_lower_title_idx", Expressions: []string{"lower(title)"}},
	}, table.Indexes)
}

// TestPostgreSQLInspectorNormalizesPartialIndexPredicateAgainstLiveDatabase
// and TestPostgreSQLInspectorNormalizesExpressionIndexKeyAgainstLiveDatabase
// prove that PostgreSQL reports a partial index's predicate, and an
// expression key's text, back through pg_catalog.pg_get_expr and
// pg_catalog.pg_get_indexdef in the server's own re-serialized, normalized
// form, not the source text a migration wrote — the same normalization
// docs/02-schema.md already documents for ColumnDef.GeneratedExpression.
// TestPostgreSQLInspectorRecordsPartialIndexAgainstLiveDatabase and
// TestPostgreSQLInspectorRecordsExpressionIndexAgainstLiveDatabase above use
// inputs that already happen to match their own normalized form (a bare
// boolean column, a single function call), so neither actually exercises
// re-parenthesization; celsius * 9 / 5 + 32 is the exact expression
// docs/02-schema.md already pins for GeneratedExpression, reused here so the
// same normalization is pinned for an index predicate and an expression key
// too.
func TestPostgreSQLInspectorNormalizesPartialIndexPredicateAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, celsius integer NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_hot_idx ON articles (id) WHERE celsius * 9 / 5 + 32 > 100")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_hot_idx", Columns: []string{"id"}, Predicate: "(((celsius * 9) / 5) + 32) > 100"},
	}, table.Indexes)
}

func TestPostgreSQLInspectorNormalizesExpressionIndexKeyAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, celsius integer NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_fahrenheit_idx ON articles ((celsius * 9 / 5 + 32))")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_fahrenheit_idx", Expressions: []string{"(((celsius * 9) / 5) + 32)"}},
	}, table.Indexes)
}

// TestMySQLInspectorRecordsFunctionalIndexAgainstLiveDatabase pins what a
// real MySQL server reports back in information_schema.statistics.expression
// for a functional index key, the same way index_method_integration_test.go
// pins index_type for a FULLTEXT index: before this feature existed,
// inspecting this table failed outright, the exact failure a sweep over a
// production schema must not hit on its first functional index.
func TestMySQLInspectorRecordsFunctionalIndexAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id INT PRIMARY KEY, title VARCHAR(255) NOT NULL) ENGINE=InnoDB")
	mustExec(t, ctx, database, "CREATE INDEX articles_lower_title_idx ON articles ((lower(title)))")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Len(t, table.Indexes, 1)
	require.Equal(t, "articles_lower_title_idx", table.Indexes[0].Name)
	require.Empty(t, table.Indexes[0].Columns)
	require.Len(t, table.Indexes[0].Expressions, 1)
	require.Contains(t, table.Indexes[0].Expressions[0], "title")
}
