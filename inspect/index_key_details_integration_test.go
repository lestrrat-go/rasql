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

// TestPostgreSQLInspectorRecordsIndexKeyDetailsAgainstLiveDatabase pins what
// a real PostgreSQL server reports back for a descending key, a per-key
// collation, a non-default operator class, and an index's INCLUDE columns,
// because TestPostgreSQLInspectorRecordsIndexKeyDetails in inspect_test.go
// only asserts rasql's own mocked catalog echoes what the test told it to.
// Before this feature existed, inspecting this table failed outright on
// whichever of these four an index reached first: the exact failure a
// sweep over a production schema must not hit.
func TestPostgreSQLInspectorRecordsIndexKeyDetailsAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, title text NOT NULL, created_at timestamp NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_created_at_desc_idx ON articles (created_at DESC)")
	mustExec(t, ctx, database, `CREATE INDEX articles_title_c_idx ON articles (title COLLATE "C")`)
	mustExec(t, ctx, database, "CREATE INDEX articles_title_pattern_idx ON articles (title text_pattern_ops)")
	mustExec(t, ctx, database, "CREATE INDEX articles_id_include_idx ON articles (id) INCLUDE (title)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_created_at_desc_idx", Keys: []schema.IndexKeyDef{{Expression: "created_at", Descending: true}}},
		{Name: "articles_id_include_idx", Columns: []string{"id"}, IncludeColumns: []string{"title"}},
		{Name: "articles_title_c_idx", Keys: []schema.IndexKeyDef{{Expression: "title", Collation: "C"}}},
		{Name: "articles_title_pattern_idx", Keys: []schema.IndexKeyDef{{Expression: "title", OperatorClass: "text_pattern_ops"}}},
	}, table.Indexes)
}

// TestMySQLInspectorRecordsIndexKeyDetailsAgainstLiveDatabase pins what a
// real MySQL server reports back for a column-prefix index part and an
// invisible index, because TestMySQLInspectorRecordsPrefixIndexPart and
// TestMySQLInspectorRecordsInvisibleNonDefaultMethodUniqueIndex in
// inspect_test.go only assert rasql's own mocked catalog echoes what the
// test told it to. compose.yaml runs mysql:8.4, so INVISIBLE, which needs
// MySQL 8.0+, is always available here.
func TestMySQLInspectorRecordsIndexKeyDetailsAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id INT PRIMARY KEY, title VARCHAR(255) NOT NULL, status VARCHAR(50) NOT NULL) ENGINE=InnoDB")
	mustExec(t, ctx, database, "CREATE INDEX articles_title_prefix_idx ON articles (title(10))")
	mustExec(t, ctx, database, "CREATE INDEX articles_status_idx ON articles (status) INVISIBLE")

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_status_idx", Columns: []string{"status"}, Invisible: true},
		{Name: "articles_title_prefix_idx", Keys: []schema.IndexKeyDef{{Expression: "title", PrefixLength: 10}}},
	}, table.Indexes)
}
