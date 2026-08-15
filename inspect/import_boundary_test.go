package inspect_test

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInspectDoesNotImportADriver states the rule inspect's callers rely on:
// importing this package must not link, and so must not register, any
// database/sql driver. inspect/inspect.go used to import
// github.com/go-sql-driver/mysql as an ordinary (non-blank) import purely to
// type-assert its error type, and that package's init registers a driver
// under the name "mysql" as a side effect of being imported at all -- so
// every program that imported inspect got the MySQL driver linked in and
// registered, whether or not it ever spoke to MySQL. See mysqlErrorNumber
// in inspect/mysql_error_number.go for how the MySQL error 1146 check now
// avoids that import. This test also covers PostgreSQL's and SQLite's
// driver packages, which inspect has never imported, so a future change
// cannot reintroduce either one unnoticed either.
func TestInspectDoesNotImportADriver(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "github.com/lestrrat-go/rasql/inspect").CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, driver := range []string{
		"github.com/go-sql-driver/mysql",
		"github.com/jackc/pgx",
		"modernc.org/sqlite",
	} {
		require.NotContains(t, string(output), driver)
	}
}
