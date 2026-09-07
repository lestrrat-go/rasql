//go:build unix

package generate_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestLiveDescriptorRoundTripsThroughGeneratedSource covers the one residue
// TestSchemaDescriptorRoundTripsThroughGeneratedSource's hand-built fixture
// cannot reach: whether a descriptor inspect.Table actually produces from a
// live PostgreSQL or MySQL table has a shape the renderer has never seen. It
// inspects a real table on each engine, renders the resulting
// schema.TableDef through the same scratch-module round trip the local test
// uses (see roundTripDescriptors in descriptor_roundtrip_test.go), and
// requires the same properties, all checked inside that helper: the
// descriptor read back through the generated accessor, inside the scratch
// module's own child process, is the schema.TableDef inspection produced
// here and is reflect.DeepEqual to the literal it was rendered from, and
// rendering it again is byte-for-byte identical to the first rendering.
//
// An inspected table that carries a foreign key with no matching
// relationship fails roundTripDescriptors' own precondition, since rasqlgen
// derives a relationship for it and the descriptor read back is then not the
// one passed in; neither table below has a foreign key. See
// requireGeneratorDerivesNoRelationship for what to do about one that does.
//
// dbtest.PostgreSQLDB and dbtest.MySQLDB each skip with instructions when
// their DSN environment variable is unset, so this test needs no database to
// compile, vet, or appear in `go test ./...` -- only to run.
func TestLiveDescriptorRoundTripsThroughGeneratedSource(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		ctx := t.Context()
		database := dbtest.PostgreSQLDB(t)
		tableName := dbtest.UniqueName(t, "rasql_generated_native")
		enumName := dbtest.UniqueName(t, "rasql_generated_mood")
		domainName := dbtest.UniqueName(t, "rasql_generated_amount")
		quote := func(value string) string { return `"` + value + `"` }
		_, err := database.ExecContext(ctx, "CREATE TYPE "+quote(enumName)+" AS ENUM ('sad', 'happy')")
		require.NoError(t, err, "create enum")
		_, err = database.ExecContext(ctx, "CREATE DOMAIN "+quote(domainName)+" AS NUMERIC(10,2)")
		require.NoError(t, err, "create domain")

		_, err = database.ExecContext(ctx, "CREATE TABLE "+quote(tableName)+" (\n"+
			"id integer PRIMARY KEY, mood "+quote(enumName)+", moods "+quote(enumName)+"[], amount "+quote(domainName)+", arbitrary numeric, payload json, payload_binary jsonb, created_at timestamp(3) with time zone,\n"+
			"CONSTRAINT "+quote(tableName+"_amount_check")+" CHECK (amount > 0)\n)")
		require.NoError(t, err, "create table")

		inspector, err := inspect.New(database, dialect.PostgreSQL())
		require.NoError(t, err, "create inspector")
		table, err := inspector.Table(ctx, tableName)
		require.NoError(t, err, "inspect table")

		roundTripDescriptors(t, table)
	})

	t.Run("mysql", func(t *testing.T) {
		ctx := t.Context()
		database := dbtest.MySQLDB(t)
		tableName := dbtest.UniqueName(t, "rasql_generated_native")

		_, err := database.ExecContext(ctx, "CREATE TABLE `"+tableName+"` (\n"+
			"id INT UNSIGNED PRIMARY KEY, mood ENUM('sad','happy'), flags SET('one','two'), amount DECIMAL(10,2) UNSIGNED, payload JSON, created_at TIMESTAMP NULL\n) ENGINE=InnoDB")
		require.NoError(t, err, "create table")

		inspector, err := inspect.New(database, dialect.MySQL())
		require.NoError(t, err, "create inspector")
		table, err := inspector.Table(ctx, tableName)
		require.NoError(t, err, "inspect table")

		roundTripDescriptors(t, table)
	})
}
