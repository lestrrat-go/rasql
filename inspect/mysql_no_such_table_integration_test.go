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
// depends on. Every sqlmock-based test of this behavior, including
// TestMySQLInspectorReportsTableNotFoundWhenSHOWCreateProvesAbsence and
// TestMySQLInspectorPropagatesOtherShowCreateTableErrors in inspect_test.go,
// constructs a local fake error struct that by definition carries the Number
// field mysqlErrorNumber looks for, so none of them can catch a real
// go-sql-driver/mysql release that renames or removes that field -- they
// would all stay green regardless. This test asks a genuinely absent
// table's SHOW CREATE TABLE to fail against a real MySQL server, so the
// error mysqlErrorNumber inspects here is the actual *mysql.MySQLError the
// driver constructs, not a stand-in, and it pins that inspector.Table still
// reports TableNotFoundError rather than propagating the raw error.
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
