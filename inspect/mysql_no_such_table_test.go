package inspect

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestMySQLInspectorReportsTableNotFoundWhenSHOWCreateProvesAbsence drives
// Table through the MySQL error 1146 path with a mock database, from the
// failing SHOW CREATE TABLE to the TableNotFoundError callers see.
//
// It lives in this package, not in inspect_test, because the driver's own
// *mysql.MySQLError cannot appear here: importing github.com/go-sql-driver/
// mysql registers a database/sql driver in every program that imports
// inspect, which is exactly what TestInspectDoesNotImportADriver forbids, and
// no test can declare a type in the driver's package either. So the test
// substitutes a local type's identity for the driver's in the Inspector,
// which changes only which type the error number is read from -- the SQL, the
// walk over the error chain, and the TableNotFoundError the inspector builds
// are the production ones. That a genuine *mysql.MySQLError still takes this
// path is pinned by TestMySQLInspectorReportsTableNotFoundAgainstLiveDatabase
// against a real server, which is the only place it can be pinned.
func TestMySQLInspectorReportsTableNotFoundWhenSHOWCreateProvesAbsence(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	require.Equal(t, mysqlDriverErrorType, inspector.mysqlErrorType, "New must install the driver's own error identity")
	inspector.mysqlErrorType = fixtureErrorType(&mysqlErrorFixture{})

	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("ghosts").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}))
	mock.ExpectQuery("SHOW CREATE TABLE `ghosts`").
		WillReturnError(&mysqlErrorFixture{Number: mysqlErrNoSuchTable, Message: "Table 'test.ghosts' doesn't exist"})

	table, err := inspector.Table(t.Context(), "ghosts")
	require.Equal(t, schema.TableDef{}, table)
	require.ErrorIs(t, err, ErrTableNotFound)
	var notFound *TableNotFoundError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, "ghosts", notFound.Table)
}
