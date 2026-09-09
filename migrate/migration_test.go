package migrate_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

// TestMigrationValidateAcceptsAnIrreversibleReasonOnly requires
// IrreversibleReason to be meaningful on a Migration built directly in Go,
// not only on one read from disk, since a caller who builds Migration
// values by hand should see the same reason a marker file would have given.
func TestMigrationValidateAcceptsAnIrreversibleReasonOnly(t *testing.T) {
	base := func() migrate.Migration {
		return migrate.Migration{ID: "001_data_change", Statements: []migrate.Statement{{Source: "001.up.sql", SQL: sqltext.Text("SELECT 1")}}}
	}

	migration := base()
	require.NoError(t, migration.Validate(), "no Down and no reason is a plain irreversible migration")

	migration = base()
	migration.IrreversibleReason = "data transformation cannot be reversed"
	require.NoError(t, migration.Validate())

	migration = base()
	migration.IrreversibleReason = "   "
	require.ErrorContains(t, migration.Validate(), "invalid irreversibility reason")

	migration = base()
	migration.IrreversibleReason = strings.Repeat("x", 4097)
	require.ErrorContains(t, migration.Validate(), "invalid irreversibility reason")

	migration = base()
	migration.Down = []migrate.Statement{{Source: "001.down.sql", SQL: sqltext.Text("SELECT 0")}}
	migration.IrreversibleReason = "kept for symmetry with the loader, though Down makes it unused"
	require.NoError(t, migration.Validate(), "a reason beside reverse sources is unusual but not invalid")
}
