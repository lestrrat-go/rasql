package inspect_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/stretchr/testify/require"
)

func TestMySQLTableInPassesNamespaceToEveryMetadataQuery(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	mock.ExpectQuery("information_schema\\.columns.*").WithArgs("audit", "events").WillReturnRows(
		sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).AddRow("id", "bigint", "NO", nil, nil, nil, "", ""),
	)
	mock.ExpectQuery("SHOW CREATE TABLE `audit`\\.`events`").WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("events", "CREATE TABLE `events` (`id` bigint NOT NULL) ENGINE=InnoDB"))
	mock.ExpectQuery("key_column_usage\\.column_name.*PRIMARY KEY").WithArgs("audit", "events").WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	mock.ExpectQuery("constraint_name.*constraint_type = 'UNIQUE'").WithArgs("audit", "events").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("check_constraints.*constraint_type = 'CHECK'").WithArgs("audit", "events").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}))
	mock.ExpectQuery("SHOW COLUMNS FROM information_schema.statistics LIKE 'EXPRESSION'").WillReturnRows(sqlmock.NewRows([]string{"Field"}).AddRow("EXPRESSION"))
	mock.ExpectQuery("SHOW COLUMNS FROM information_schema.statistics LIKE 'IS_VISIBLE'").WillReturnRows(sqlmock.NewRows([]string{"Field"}).AddRow("IS_VISIBLE"))
	mock.ExpectQuery("information_schema\\.statistics.*").WithArgs("audit", "events").WillReturnRows(sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))
	mock.ExpectQuery("key_column_usage\\.constraint_name.*referenced_table_name IS NOT NULL").WithArgs("audit", "audit", "events").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}))

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	table, err := inspector.TableIn(t.Context(), "audit", "events")
	require.NoError(t, err)
	require.Equal(t, "audit", table.Schema)
	require.Equal(t, "events", table.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}
