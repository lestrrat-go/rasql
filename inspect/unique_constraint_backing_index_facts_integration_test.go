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

// TestPostgreSQLInspectorRecordsUniqueConstraintStorageParametersAgainstLiveDatabase
// pins what a real PostgreSQL server reports back for a named unique
// constraint's backing index's storage parameters, because the
// sqlmock-based TestPostgreSQLInspectorRecordsUniqueConstraintBackingIndexFacts
// in inspect_test.go only asserts rasql's own mocked catalog echoes what
// the test told it to. Before this feature existed, inspecting this table
// failed outright: a fillfactor storage parameter on the constraint's
// backing index made the whole table unrepresentable, which is the exact
// failure a sweep over a production schema must not hit on its first such
// constraint.
func TestPostgreSQLInspectorRecordsUniqueConstraintStorageParametersAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, code text NOT NULL)")
	mustExec(t, ctx, database, "ALTER TABLE articles ADD CONSTRAINT articles_code_key UNIQUE (code) WITH (fillfactor = 70)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "articles_code_key", Columns: []string{"code"}, StorageParameters: map[string]string{"fillfactor": "70"}},
	}, table.UniqueConstraints)
}

// TestPostgreSQLInspectorRecordsUniqueConstraintReplicaIdentityAgainstLiveDatabase
// pins what a real PostgreSQL server reports back for a named unique
// constraint's backing index serving as the table's replica identity.
// Before this feature existed, inspecting this table failed outright: the
// replica identity index made the whole table unrepresentable, which is
// the exact failure a sweep over a production schema with logical
// replication configured must not hit.
func TestPostgreSQLInspectorRecordsUniqueConstraintReplicaIdentityAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, email text NOT NULL)")
	mustExec(t, ctx, database, "ALTER TABLE articles ADD CONSTRAINT articles_email_key UNIQUE (email)")
	mustExec(t, ctx, database, "ALTER TABLE articles REPLICA IDENTITY USING INDEX articles_email_key")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "articles_email_key", Columns: []string{"email"}, ReplicaIdentity: true},
	}, table.UniqueConstraints)
}

// TestPostgreSQLInspectorRecordsUniqueConstraintCollationAgainstLiveDatabase
// pins what a real PostgreSQL server reports back for a named unique
// constraint's backing index carrying a nondefault per-column collation.
// The "C" collation is built into every PostgreSQL installation regardless
// of locale, so this test does not depend on the server's own default
// locale to produce a mismatch: a column explicitly declared COLLATE "C"
// always differs from text's own default (locale-dependent) collation.
// Before this feature existed, inspecting this table failed outright: the
// nondefault collation on the constraint's backing index made the whole
// table unrepresentable, which is the exact failure a sweep over a
// production schema must not hit on its first such constraint.
func TestPostgreSQLInspectorRecordsUniqueConstraintCollationAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, `CREATE TABLE articles (id integer PRIMARY KEY, code text COLLATE "C" NOT NULL)`)
	mustExec(t, ctx, database, "ALTER TABLE articles ADD CONSTRAINT articles_code_key UNIQUE (code)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "articles_code_key", Columns: []string{"code"}, Collations: map[string]string{"code": "C"}},
	}, table.UniqueConstraints)
}
