package inspect_test

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/stretchr/testify/require"
)

func TestScopedTableNamesUseRequestedPostgreSQLSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	query := `SELECT table_data.relname FROM pg_catalog.pg_class AS table_data JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace WHERE table_namespace.nspname = $1 AND table_data.relkind IN ('r','p') ORDER BY table_data.relname`
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("billing").WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("events"))
	inspector, err := inspect.New(db, dialect.PostgreSQL())
	require.NoError(t, err)
	refs, err := inspector.TableNamesIn(t.Context(), "billing")
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "billing", Name: "events"}}, refs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScopedTableNamesUseRequestedMySQLDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	query := "SELECT table_name FROM information_schema.tables WHERE table_schema = ? AND table_type = 'BASE TABLE' ORDER BY table_name"
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("audit").WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("events"))
	inspector, err := inspect.New(db, dialect.MySQL())
	require.NoError(t, err)
	refs, err := inspector.TableNamesIn(t.Context(), "audit")
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "audit", Name: "events"}}, refs)
	require.NoError(t, mock.ExpectationsWereMet())
}
