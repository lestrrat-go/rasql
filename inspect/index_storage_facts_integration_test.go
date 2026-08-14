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

// TestPostgreSQLInspectorRecordsStorageParametersAgainstLiveDatabase pins
// what a real PostgreSQL server reports back for an index's storage
// parameters, because the sqlmock-based
// TestPostgreSQLInspectorRecordsIndexValidityStorageAndPlacement in
// inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to. Before this feature existed, inspecting this table
// failed outright: a fillfactor storage parameter made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema must not hit on its first index carrying one.
func TestPostgreSQLInspectorRecordsStorageParametersAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, title text NOT NULL)")
	mustExec(t, ctx, database, "CREATE INDEX articles_title_idx ON articles (title) WITH (fillfactor = 70)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_title_idx", Columns: []string{"title"}, StorageParameters: map[string]string{"fillfactor": "70"}},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsReplicaIdentityAgainstLiveDatabase pins what
// a real PostgreSQL server reports back for a table's replica identity
// index. Before this feature existed, inspecting this table failed
// outright: the replica identity index made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema with logical replication configured must not hit.
func TestPostgreSQLInspectorRecordsReplicaIdentityAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE articles (id integer PRIMARY KEY, email text NOT NULL)")
	mustExec(t, ctx, database, "CREATE UNIQUE INDEX articles_email_uidx ON articles (email)")
	mustExec(t, ctx, database, "ALTER TABLE articles REPLICA IDENTITY USING INDEX articles_email_uidx")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "articles")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "articles_email_uidx", Columns: []string{"email"}, Unique: true, ReplicaIdentity: true},
	}, table.Indexes)
}
