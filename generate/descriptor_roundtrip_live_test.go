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

		_, err := database.ExecContext(ctx, `CREATE TABLE widgets (
			id integer PRIMARY KEY,
			name varchar(255) NOT NULL,
			amount numeric(10,2) NOT NULL,
			created_at timestamp NOT NULL DEFAULT now(),
			CONSTRAINT widgets_amount_check CHECK (amount > 0)
		)`)
		require.NoError(t, err, "create table")
		_, err = database.ExecContext(ctx, `CREATE UNIQUE INDEX widgets_name_idx ON widgets (name)`)
		require.NoError(t, err, "create index")

		inspector, err := inspect.New(database, dialect.PostgreSQL())
		require.NoError(t, err, "create inspector")
		table, err := inspector.Table(ctx, "widgets")
		require.NoError(t, err, "inspect table")

		roundTripDescriptors(t, table)
	})

	t.Run("mysql", func(t *testing.T) {
		ctx := t.Context()
		database := dbtest.MySQLDB(t)

		_, err := database.ExecContext(ctx, `CREATE TABLE widgets (
			id INT UNSIGNED PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			amount DECIMAL(10,2) UNSIGNED NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY widgets_name_idx (name)
		) ENGINE=InnoDB`)
		require.NoError(t, err, "create table")

		inspector, err := inspect.New(database, dialect.MySQL())
		require.NoError(t, err, "create inspector")
		table, err := inspector.Table(ctx, "widgets")
		require.NoError(t, err, "inspect table")

		roundTripDescriptors(t, table)
	})
}
