//go:build unix

package catalogread_test

import (
	"context"
	"testing"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
)

// TestNamespaceScopedReadReturnsOnlyThatNamespace confirms Scope.Namespaces
// actually selects a namespace against a real server, on both engines this
// repository supports. tables.namespaces (docs/orm/01-codegen.md:68-69) has
// always been documented, but nothing ever read it, so this is the proof
// that the fix does what the owner's PostgreSQL analytics cluster needs: a
// read scoped to one schema must not also pick up tables that merely share
// the connection's default namespace.
func TestNamespaceScopedReadReturnsOnlyThatNamespace(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		db := dbtest.PostgreSQLDB(t)
		ctx := t.Context()
		schemaName := dbtest.UniqueName(t, "rasql_ns")

		_, err := db.ExecContext(ctx, "CREATE TABLE default_schema_table (id INTEGER)")
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `CREATE SCHEMA "`+schemaName+`"`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schemaName+`" CASCADE`)
		})
		_, err = db.ExecContext(ctx, `CREATE TABLE "`+schemaName+`".scoped_table (id INTEGER)`)
		require.NoError(t, err)

		profile, err := engineprofile.DiscoverBuiltin(ctx, db, engineprofile.PostgreSQL)
		require.NoError(t, err)

		result, err := catalogread.Read(ctx, db, profile, catalogread.Scope{Namespaces: []string{schemaName}})
		require.NoError(t, err)
		require.Len(t, result.Tables, 1)
		require.Equal(t, schemaName, result.Tables[0].Schema)
		require.Equal(t, "scoped_table", result.Tables[0].Name)
	})

	t.Run("mysql", func(t *testing.T) {
		db := dbtest.MySQLDB(t)
		ctx := t.Context()
		databaseName := dbtest.UniqueName(t, "rasql_ns")

		_, err := db.ExecContext(ctx, "CREATE TABLE default_database_table (id INTEGER)")
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, "CREATE DATABASE `"+databaseName+"`")
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+databaseName+"`")
		})
		_, err = db.ExecContext(ctx, "CREATE TABLE `"+databaseName+"`.scoped_table (id INTEGER)")
		require.NoError(t, err)

		profile, err := engineprofile.DiscoverBuiltin(ctx, db, engineprofile.MySQL)
		require.NoError(t, err)

		result, err := catalogread.Read(ctx, db, profile, catalogread.Scope{Namespaces: []string{databaseName}})
		require.NoError(t, err)
		require.Len(t, result.Tables, 1)
		require.Equal(t, databaseName, result.Tables[0].Schema)
		require.Equal(t, "scoped_table", result.Tables[0].Name)
	})
}
