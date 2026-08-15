//go:build unix

package inspect_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestMySQLInspectorReportsTableNotFoundAgainstLiveDatabase drives the real
// MySQL error 1146 path that mysqlErrorNumber (inspect/mysql_error_number.go)
// depends on. No sqlmock-based test can: mysqlErrorNumber reads a number only
// from github.com/go-sql-driver/mysql's own *mysql.MySQLError, identified by
// package path and type name, and no test may declare a type in that package
// or import it -- TestInspectDoesNotImportADriver forbids the import outright.
// So TestMySQLInspectorReportsTableNotFoundWhenSHOWCreateProvesAbsence in
// mysql_no_such_table_test.go substitutes a local type's identity for the
// driver's, and TestMySQLInspectorPropagatesOtherShowCreateTableErrors in
// inspect_test.go feeds types the driver did not declare and expects them to
// propagate. Neither can catch a go-sql-driver/mysql release that moves,
// renames, or reshapes that error type -- both would stay green regardless.
// This test asks a genuinely absent table's SHOW CREATE TABLE to fail against
// a real MySQL server, so the error mysqlErrorNumber inspects here is the
// actual *mysql.MySQLError the driver constructs, not a stand-in, and it pins
// that inspector.Table still reports TableNotFoundError rather than
// propagating the raw error.
func TestMySQLInspectorReportsTableNotFoundAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	database := dbtest.MySQLDB(t)

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)

	_, err = inspector.Table(ctx, "definitely_absent_table")
	require.ErrorIs(t, err, inspect.ErrTableNotFound)
	var notFound *inspect.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, "definitely_absent_table", notFound.Table)
}
