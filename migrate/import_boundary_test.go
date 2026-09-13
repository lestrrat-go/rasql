package migrate_test

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigrateDoesNotImportADriver states the rule migrate's callers rely on:
// importing this package must not link, and so must not register, any
// database/sql driver. migrate reads MySQL error numbers through
// internal/mysqlerrno, which names the driver's error type by package path
// rather than importing it, because go-sql-driver/mysql registers a driver
// under the name "mysql" as a side effect of being imported at all.
func TestMigrateDoesNotImportADriver(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "github.com/lestrrat-go/rasql/migrate").CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, driver := range []string{
		"github.com/go-sql-driver/mysql",
		"github.com/jackc/pgx",
		"modernc.org/sqlite",
	} {
		require.NotContains(t, string(output), driver)
	}
}
