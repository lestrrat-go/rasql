package schemasource_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestReadClearsTheSQLiteMainDatabaseAndKeepsAnAttachedOne reads a real SQLite database that has
// a second database attached under the name "audit", and pins both halves of the rule at once:
// the table in "main" comes back unqualified, so a generated store renders it as "users" rather
// than "main"."users", and the table in "audit" keeps its namespace, so a statement built from
// that descriptor still names the database it has to reach.
//
// The read runs through schemasource.DefaultDependencies with only the opener replaced, so the
// catalog, the profile and the namespace all come from this connection rather than from a
// fixture. ATTACH binds to one connection, which is why the pool is capped at one.
func TestReadClearsTheSQLiteMainDatabaseAndKeepsAnAttachedOne(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rwc", filepath.Join(dir, "main.db")))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), fmt.Sprintf("ATTACH DATABASE 'file:%s?mode=rwc' AS audit", filepath.Join(dir, "audit.db")))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "CREATE TABLE audit.events (id INTEGER NOT NULL PRIMARY KEY, action TEXT NOT NULL)")
	require.NoError(t, err)

	deps := schemasource.DefaultDependencies()
	deps.Opener = fakeDBOpener{db: db}
	req := schemasource.ReadRequest{ModuleRoot: t.TempDir(), Dialect: "sqlite", DSN: "supplied-by-the-opener"}
	result, err := schemasource.Read(t.Context(), req, deps)
	require.NoError(t, err)

	namespaces := map[string]string{}
	for _, object := range result.Catalog.Objects {
		namespaces[object.Name] = object.Schema
	}
	require.Equal(t, map[string]string{"users": "", "events": "audit"}, namespaces)
}
