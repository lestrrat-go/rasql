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

// TestPostgreSQLInspectorRecordsDeferrableForeignKeyAgainstLiveDatabase pins
// what a real PostgreSQL server reports back for a DEFERRABLE INITIALLY
// DEFERRED foreign key, because TestPostgreSQLInspectorRecordsForeignKeyFacts
// in inspect_test.go only asserts rasql's own mocked catalog echoes what the
// test told it to. Before this feature existed, inspecting this table failed
// outright: the deferrable foreign key made the whole table
// unrepresentable, which is the exact failure a sweep over a production
// schema must not hit on its first deferrable foreign key.
func TestPostgreSQLInspectorRecordsDeferrableForeignKeyAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	mustExec(t, ctx, database, "CREATE TABLE accounts (id integer PRIMARY KEY)")
	mustExec(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, account_id integer REFERENCES accounts (id) DEFERRABLE INITIALLY DEFERRED)")

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "users")
	require.NoError(t, err)
	require.Len(t, table.ForeignKeys, 1)
	require.Equal(t, "accounts", table.ForeignKeys[0].ReferencedTable)
	require.Equal(t, schema.DeferrableInitiallyDeferred, table.ForeignKeys[0].Deferrable)
}

// TestPostgreSQLInspectorRecordsCrossSchemaForeignKeyAgainstLiveDatabase pins
// what a real PostgreSQL server reports back for a foreign key referencing a
// table in another schema. Before this feature existed, inspecting this
// table failed outright: a referenced table outside the current schema made
// the whole table unrepresentable.
func TestPostgreSQLInspectorRecordsCrossSchemaForeignKeyAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.PostgreSQLDB(t)

	billingSchema := dbtest.UniqueName(t, "rasql_billing")
	mustExec(t, ctx, database, `CREATE SCHEMA "`+billingSchema+`"`)
	mustExec(t, ctx, database, `CREATE TABLE "`+billingSchema+`".accounts (id integer PRIMARY KEY)`)
	mustExec(t, ctx, database, `CREATE TABLE users (id integer PRIMARY KEY, account_id integer REFERENCES "`+billingSchema+`".accounts (id))`)

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, "users")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{{
		Name:              table.ForeignKeys[0].Name,
		Columns:           []string{"account_id"},
		ReferencedSchema:  billingSchema,
		ReferencedTable:   "accounts",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.NoAction,
		OnUpdate:          schema.NoAction,
	}}, table.ForeignKeys)
}
