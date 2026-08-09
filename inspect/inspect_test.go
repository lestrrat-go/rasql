package inspect_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestPostgreSQLInspectorNormalizesColumnsAndPrimaryKey(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil).
			AddRow("email", "character varying", "YES", driver.Value(nil), nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "users", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "users")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.Nil(t, table.UniqueConstraints)
	require.Nil(t, table.Checks)
	require.Nil(t, table.Indexes)
	require.Nil(t, table.ForeignKeys)
}

func TestPostgreSQLInspectorNormalizesNumericColumn(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("amount", "numeric", "NO", nil, int64(19), int64(4)))
	expectPostgreSQLCatalogColumnCount(mock, "payments", 1)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}))
	expectPostgreSQLEmptyMetadata(mock, "payments")

	table, err := inspector.Table(t.Context(), "payments")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
	}, table.Columns)
}

func TestPostgreSQLInspectorRejectsUnconstrainedNumericColumn(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("amount", "numeric", "NO", nil, nil, nil))

	_, err = inspector.Table(t.Context(), "payments")
	require.ErrorContains(t, err, "unconstrained NUMERIC has no precision to record")
	require.ErrorContains(t, err, `"amount"`)
}

// TestPostgreSQLInspectorRejectsDecimalColumnWithoutScale covers a catalog row
// that reports a precision but a NULL scale. Reading the NULL as 0 would turn
// a NUMERIC(10,2) column into a descriptor that re-renders as NUMERIC(10,0)
// and drops the column's fractional digits, so inspection refuses it instead.
func TestPostgreSQLInspectorRejectsDecimalColumnWithoutScale(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("amount", "numeric", "NO", nil, int64(10), nil))

	_, err = inspector.Table(t.Context(), "payments")
	require.ErrorContains(t, err, "reports no scale to record")
	require.ErrorContains(t, err, `"amount"`)
}

func TestPostgreSQLInspectorPreservesSupportedMetadata(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil).
			AddRow("account_id", "bigint", "NO", nil, nil, nil).
			AddRow("tenant_id", "bigint", "NO", nil, nil, nil).
			AddRow("email", "character varying", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "users", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}).
			AddRow("uq_users_email", "email", false, false, false, false, false, false).
			AddRow("uq_users_tenant_email", "tenant_id", false, false, false, false, false, false).
			AddRow("uq_users_tenant_email", "email", false, false, false, false, false, false))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}).
			AddRow("chk_users_email", "email <> ''", false, true, true))
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*NOT index_metadata\\.indisvalid.*index_data\\.reloptions IS NOT NULL.*index_data\\.reltablespace <> 0.*index_metadata\\.indnullsnotdistinct.*index_metadata\\.indisreplident.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation.*attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname FROM pg_catalog\\.pg_index.*index_data\\.reloptions IS NULL.*index_data\\.reltablespace = 0.*NOT index_metadata\\.indisreplident").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname"}).
			AddRow("users_email_idx", false, "email").
			AddRow("users_tenant_email_idx", true, "tenant_id").
			AddRow("users_tenant_email_idx", true, "email"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL, constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "c", "a", "s", true, false, false, false, true, true, false).
			AddRow("fk_users_account", "tenant_id", "accounts", "tenant_id", "c", "a", "s", true, false, false, false, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueConstraint{
		{Name: "uq_users_email", Columns: []string{"email"}},
		{Name: "uq_users_tenant_email", Columns: []string{"tenant_id", "email"}},
	}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckConstraint{
		{Name: "chk_users_email", Expression: "email <> ''"},
	}, table.Checks)
	require.Equal(t, []schema.Index{
		{Name: "users_email_idx", Columns: []string{"email"}},
		{Name: "users_tenant_email_idx", Columns: []string{"tenant_id", "email"}, Unique: true},
	}, table.Indexes)
	require.Equal(t, []schema.ForeignKey{
		{
			Name:              "fk_users_account",
			Columns:           []string{"account_id", "tenant_id"},
			ReferencedTable:   "accounts",
			ReferencedColumns: []string{"id", "tenant_id"},
			OnDelete:          schema.ReferenceActionCascade,
			OnUpdate:          schema.ReferenceActionNoAction,
		},
	}, table.ForeignKeys)

	source, err := generate.Schema("generated", table)
	require.NoError(t, err)
	require.Contains(t, string(source), "UniqueConstraints: []schema.UniqueConstraint{")
	require.Contains(t, string(source), "Checks: []schema.CheckConstraint{")
	require.Contains(t, string(source), "Indexes: []schema.Index{")
	require.Contains(t, string(source), "ForeignKeys: []schema.ForeignKey{")
}

func TestPostgreSQLInspectorRejectsReplicaIdentityIndex(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "users", 1)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*NOT index_metadata\\.indisvalid.*index_data\\.reloptions IS NOT NULL.*index_data\\.reltablespace <> 0.*index_metadata\\.indnullsnotdistinct.*index_metadata\\.indisreplident.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation.*attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("users_email_replica_identity_idx"))

	_, err = inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: index \"users_email_replica_identity_idx\" cannot be represented: rasql supports only valid, non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, default persistent storage options and tablespaces, distinct nulls, and no replica identity")
}

func TestPostgreSQLInspectorRejectsInvalidIndex(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*NOT index_metadata\\.indisvalid.*index_data\\.reloptions IS NOT NULL.*index_data\\.reltablespace <> 0.*index_metadata\\.indnullsnotdistinct.*index_metadata\\.indisreplident.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation.*attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("users_email_invalid_idx"))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: index \"users_email_invalid_idx\" cannot be represented: rasql supports only valid, non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, default persistent storage options and tablespaces, distinct nulls, and no replica identity")
}

func TestPostgreSQLInspectorRejectsExclusionConstraintBeforeIndexInspection(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname"}).AddRow("excl_users_booking"))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: exclusion constraint \"excl_users_booking\" cannot be represented: rasql does not support exclusion constraints")
}

func TestPostgreSQLInspectorRejectsIndexWithPersistentStorageOptionsOrTablespace(t *testing.T) {
	tests := []struct {
		name       string
		queryMatch string
		indexName  string
	}{
		{name: "persistent storage options", queryMatch: "index_data\\.reloptions IS NOT NULL", indexName: "users_email_options_idx"},
		{name: "nondefault tablespace", queryMatch: "index_data\\.reltablespace <> 0", indexName: "users_email_tablespace_idx"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLServerVersion(mock, "180000")
			expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
			mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
			mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
			mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname"}))
			mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*" + test.queryMatch).
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow(test.indexName))

			_, err := inspector.Table(t.Context(), "users")
			require.EqualError(t, err, "inspect: index \""+test.indexName+"\" cannot be represented: rasql supports only valid, non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, default persistent storage options and tablespaces, distinct nulls, and no replica identity")
		})
	}
}

func TestPostgreSQLInspectorRejectsIndexWithNondefaultColumnCollation(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname.*attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*pg_catalog\\.pg_attribute AS attribute.*JOIN pg_catalog\\.pg_type AS type_data ON type_data\\.oid = attribute\\.atttypid.*index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("users_email_collation_idx"))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: index \"users_email_collation_idx\" cannot be represented: rasql supports only valid, non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, default persistent storage options and tablespaces, distinct nulls, and no replica identity")
}

func TestPostgreSQLInspectorUsesPostgreSQL14CatalogQueries(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "140000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	expectPostgreSQLMetadataBeforeForeignKeysWithChecks(mock, "users", "140000", sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}).
		AddRow("chk_users_email", "email <> ''", false, true, true))
	expectPostgreSQLForeignKeysWithRows(mock, "users", "140000", sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
		AddRow("fk_users_account", "id", "accounts", "id", "a", "a", "s", true, false, false, false, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.CheckConstraint{{Name: "chk_users_email", Expression: "email <> ''"}}, table.Checks)
	require.Equal(t, []schema.ForeignKey{{
		Name:              "fk_users_account",
		Columns:           []string{"id"},
		ReferencedTable:   "accounts",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.ReferenceActionNoAction,
		OnUpdate:          schema.ReferenceActionNoAction,
	}}, table.ForeignKeys)
}

func TestPostgreSQL12InspectorRejectsUniqueConstraintWithNondefaultIndexCollation(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "120000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, FALSE, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, FALSE, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint.*JOIN LATERAL unnest\\(index_metadata\\.indcollation::oid\\[\\]\\) WITH ORDINALITY AS index_collation\\(collation_oid, ordinal_position\\) ON index_collation\\.ordinal_position = key_column\\.ordinal_position").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}).
			AddRow("uq_users_email", "email", false, false, false, false, false, true))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support unique constraints whose backing indexes use nondefault collations, storage options or tablespaces, or replica identity")
}

func TestPostgreSQLInspectorRejectsUnsupportedUniqueConstraint(t *testing.T) {
	tests := []struct {
		name                     string
		deferrable               bool
		initiallyDeferred        bool
		nullsNotDistinct         bool
		includesColumns          bool
		temporal                 bool
		unsupportedIndexMetadata bool
		want                     string
	}{
		{name: "deferrable", deferrable: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql supports only non-deferrable unique constraints with distinct nulls"},
		{name: "initially deferred", deferrable: true, initiallyDeferred: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql supports only non-deferrable unique constraints with distinct nulls"},
		{name: "nulls not distinct", nullsNotDistinct: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql supports only non-deferrable unique constraints with distinct nulls"},
		{name: "included columns", includesColumns: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support unique constraints with included columns"},
		{name: "temporal", temporal: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support temporal unique constraints"},
		{name: "backing index persistent storage options", unsupportedIndexMetadata: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support unique constraints whose backing indexes use nondefault collations, storage options or tablespaces, or replica identity"},
		{name: "backing index nondefault tablespace", unsupportedIndexMetadata: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support unique constraints whose backing indexes use nondefault collations, storage options or tablespaces, or replica identity"},
		{name: "backing index replica identity", unsupportedIndexMetadata: true, want: "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support unique constraints whose backing indexes use nondefault collations, storage options or tablespaces, or replica identity"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLServerVersion(mock, "180000")
			expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
			mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}).
					AddRow("uq_users_email", "email", test.deferrable, test.initiallyDeferred, test.nullsNotDistinct, test.includesColumns, test.temporal, test.unsupportedIndexMetadata))

			_, err := inspector.Table(t.Context(), "users")
			require.EqualError(t, err, test.want)
		})
	}
}

func TestPostgreSQLInspectorRejectsUniqueConstraintWithNondefaultColumnCollation(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint.*JOIN pg_catalog\\.pg_type AS type_data ON type_data\\.oid = attribute\\.atttypid").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}).
			AddRow("uq_users_email", "email", false, false, false, false, false, true))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql does not support unique constraints whose backing indexes use nondefault collations, storage options or tablespaces, or replica identity")
}

func TestPostgreSQLInspectorRejectsUnsupportedCheck(t *testing.T) {
	tests := []struct {
		name      string
		noInherit bool
		validated bool
		enforced  bool
		want      string
	}{
		{name: "no inherit", noInherit: true, validated: true, enforced: true, want: "inspect: check constraint \"chk_users_email\" cannot be represented: rasql does not support NO INHERIT check constraints"},
		{name: "not valid", validated: false, enforced: true, want: "inspect: check constraint \"chk_users_email\" cannot be represented: rasql does not support NOT VALID check constraints"},
		{name: "not enforced", validated: true, enforced: false, want: "inspect: check constraint \"chk_users_email\" cannot be represented: rasql does not support NOT ENFORCED check constraints"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLServerVersion(mock, "180000")
			expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
			mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, constraint_data\\.conperiod, index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
			mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}).
					AddRow("chk_users_email", "email <> ''", test.noInherit, test.validated, test.enforced))

			_, err := inspector.Table(t.Context(), "users")
			require.EqualError(t, err, test.want)
		})
	}
}

func TestPostgreSQLInspectorRejectsUnsupportedForeignKey(t *testing.T) {
	tests := []struct {
		name              string
		matchType         string
		inCurrentSchema   bool
		deferrable        bool
		initiallyDeferred bool
		deleteSetColumns  bool
		validated         bool
		enforced          bool
		temporal          bool
		want              string
	}{
		{name: "match full", matchType: "f", inCurrentSchema: true, validated: true, enforced: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports only MATCH SIMPLE foreign keys"},
		{name: "referenced table outside current schema", matchType: "s", validated: true, enforced: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports references only in the current schema"},
		{name: "deferrable", matchType: "s", inCurrentSchema: true, deferrable: true, validated: true, enforced: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports only non-deferrable foreign keys"},
		{name: "initially deferred", matchType: "s", inCurrentSchema: true, deferrable: true, initiallyDeferred: true, validated: true, enforced: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports only non-deferrable foreign keys"},
		{name: "partial delete set columns", matchType: "s", inCurrentSchema: true, deleteSetColumns: true, validated: true, enforced: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql does not support column lists for ON DELETE SET NULL or SET DEFAULT"},
		{name: "not valid", matchType: "s", inCurrentSchema: true, enforced: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql does not support NOT VALID foreign keys"},
		{name: "not enforced", matchType: "s", inCurrentSchema: true, validated: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql does not support NOT ENFORCED foreign keys"},
		{name: "temporal", matchType: "s", inCurrentSchema: true, validated: true, enforced: true, temporal: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql does not support temporal foreign keys"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLServerVersion(mock, "180000")
			expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
			expectPostgreSQLMetadataBeforeForeignKeys(mock, "users", "180000")
			mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL, constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
					AddRow("fk_users_account", "account_id", "accounts", "id", "a", "a", test.matchType, test.inCurrentSchema, test.deferrable, test.initiallyDeferred, test.deleteSetColumns, test.validated, test.enforced, test.temporal))

			_, err := inspector.Table(t.Context(), "users")
			require.EqualError(t, err, test.want)
		})
	}
}

func newPostgreSQLInspector(t *testing.T) (inspect.Inspector, sqlmock.Sqlmock) {
	t.Helper()

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	return inspector, mock
}

func expectPostgreSQLColumnsAndPrimaryKey(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, tableName, 1)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
}

func expectPostgreSQLServerVersion(mock sqlmock.Sqlmock, version string) {
	mock.ExpectQuery("SHOW server_version_num").
		WillReturnRows(sqlmock.NewRows([]string{"server_version_num"}).AddRow(version))
}

func expectPostgreSQLEmptyMetadata(mock sqlmock.Sqlmock, tableName string) {
	expectPostgreSQLMetadataBeforeForeignKeys(mock, tableName, "180000")
	expectPostgreSQLForeignKeys(mock, tableName, "180000")
}

func expectPostgreSQLMetadataBeforeForeignKeys(mock sqlmock.Sqlmock, tableName string, version string) {
	expectPostgreSQLMetadataBeforeForeignKeysWithChecks(mock, tableName, version, sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
}

func expectPostgreSQLMetadataBeforeForeignKeysWithChecks(mock sqlmock.Sqlmock, tableName string, version string, checks *sqlmock.Rows) {
	uniqueNulls := "index_metadata\\.indnullsnotdistinct"
	temporal := "constraint_data\\.conperiod"
	if version == "140000" {
		uniqueNulls = "FALSE"
		temporal = "FALSE"
	}
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, " + uniqueNulls + ", index_metadata\\.indnkeyatts <> index_metadata\\.indnatts, " + temporal + ", index_data\\.reloptions IS NOT NULL OR index_data\\.reltablespace <> 0 OR index_metadata\\.indisreplident OR index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "unsupported_index_metadata"}))
	enforced := "constraint_data\\.conenforced"
	if version == "140000" {
		enforced = "TRUE"
	}
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, " + enforced + " FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(checks)
	mock.ExpectQuery("SELECT constraint_data\\.conname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"conname"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*NOT index_metadata\\.indisvalid.*index_data\\.reloptions IS NOT NULL.*index_data\\.reltablespace <> 0.*" + uniqueNulls + ".*index_metadata\\.indisreplident.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation.*attribute\\.attcollation <> type_data\\.typcollation").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname FROM pg_catalog\\.pg_index.*index_data\\.reloptions IS NULL.*index_data\\.reltablespace = 0.*NOT index_metadata\\.indisreplident").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname"}))
}

func expectPostgreSQLForeignKeys(mock sqlmock.Sqlmock, tableName string, version string) {
	expectPostgreSQLForeignKeysWithRows(mock, tableName, version, sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}))
}

func expectPostgreSQLForeignKeysWithRows(mock sqlmock.Sqlmock, tableName string, version string, foreignKeys *sqlmock.Rows) {
	deleteSetColumns := "constraint_data\\.confdelsetcols IS NOT NULL"
	temporal := "constraint_data\\.conperiod"
	if version == "140000" {
		deleteSetColumns = "FALSE"
		temporal = "FALSE"
	}
	enforced := "constraint_data\\.conenforced"
	if version == "140000" {
		enforced = "TRUE"
	}
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, " + deleteSetColumns + ", constraint_data\\.convalidated, " + enforced + ", " + temporal + " FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(foreignKeys)
}

func TestMySQLInspectorNormalizesBooleanAndTinyIntColumns(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil).
			AddRow("active", "tinyint(1)", "NO", nil, nil, nil).
			AddRow("login_attempts", "tinyint", "NO", nil, nil, nil))
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "users")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "active", Type: schema.BooleanType{}},
		{Name: "login_attempts", Type: schema.IntegerType{}},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)

	source, err := generate.Schema("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Active\s+bool$`, string(source))
	require.Regexp(t, `(?m)^\s*LoginAttempts\s+int64$`, string(source))
	require.Contains(t, string(source), `row.Assign(src, "active", &r.Active)`)
	require.Contains(t, string(source), `row.Assign(src, "login_attempts", &r.LoginAttempts)`)
}

func expectMySQLEmptyMetadata(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT key_column_usage.constraint_name, key_column_usage.column_name, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'UNIQUE' ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "unsupported_index_metadata"}))
	mock.ExpectQuery("SELECT check_constraints.constraint_name, check_constraints.check_clause, FALSE, TRUE, table_constraints.enforced = 'YES' FROM information_schema.check_constraints JOIN information_schema.table_constraints ON table_constraints.constraint_name = check_constraints.constraint_name AND table_constraints.table_schema = check_constraints.constraint_schema AND table_constraints.table_name = check_constraints.table_name WHERE check_constraints.constraint_schema = DATABASE() AND check_constraints.table_name = ? AND table_constraints.constraint_type = 'CHECK' ORDER BY check_constraints.constraint_name").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}))
}

func expectMySQLIndexes(mock sqlmock.Sqlmock, tableName string) {
	expectMySQLEmptyMetadata(mock, tableName)
	mock.ExpectQuery("SELECT index_name, 0, column_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' AND non_unique = 1 ORDER BY index_name, seq_in_index").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"index_name", "0", "column_name"}))
	mock.ExpectQuery("SELECT key_column_usage.constraint_name, key_column_usage.column_name, key_column_usage.referenced_table_name, key_column_usage.referenced_column_name, CASE referential_constraints.delete_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.delete_rule END, CASE referential_constraints.update_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.update_rule END, CASE referential_constraints.match_option WHEN 'NONE' THEN 's' ELSE referential_constraints.match_option END, key_column_usage.referenced_table_schema = DATABASE(), FALSE, FALSE, FALSE, TRUE, TRUE, FALSE FROM information_schema.key_column_usage JOIN information_schema.referential_constraints ON referential_constraints.constraint_schema = key_column_usage.constraint_schema AND referential_constraints.constraint_name = key_column_usage.constraint_name AND referential_constraints.table_name = key_column_usage.table_name WHERE key_column_usage.constraint_schema = DATABASE() AND key_column_usage.table_name = ? AND key_column_usage.referenced_table_name IS NOT NULL ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_in_current_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}))
}

func expectMySQLIndexesOnly(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT index_name, 0, column_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' AND non_unique = 1 ORDER BY index_name, seq_in_index").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"index_name", "0", "column_name"}))
}

func TestMySQLInspectorReadsConstraints(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	uniqueQuery := "SELECT key_column_usage.constraint_name, key_column_usage.column_name, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'UNIQUE' ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position"
	checksQuery := "SELECT check_constraints.constraint_name, check_constraints.check_clause, FALSE, TRUE, table_constraints.enforced = 'YES' FROM information_schema.check_constraints JOIN information_schema.table_constraints ON table_constraints.constraint_name = check_constraints.constraint_name AND table_constraints.table_schema = check_constraints.constraint_schema AND table_constraints.table_name = check_constraints.table_name WHERE check_constraints.constraint_schema = DATABASE() AND check_constraints.table_name = ? AND table_constraints.constraint_type = 'CHECK' ORDER BY check_constraints.constraint_name"
	foreignKeysQuery := "SELECT key_column_usage.constraint_name, key_column_usage.column_name, key_column_usage.referenced_table_name, key_column_usage.referenced_column_name, CASE referential_constraints.delete_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.delete_rule END, CASE referential_constraints.update_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.update_rule END, CASE referential_constraints.match_option WHEN 'NONE' THEN 's' ELSE referential_constraints.match_option END, key_column_usage.referenced_table_schema = DATABASE(), FALSE, FALSE, FALSE, TRUE, TRUE, FALSE FROM information_schema.key_column_usage JOIN information_schema.referential_constraints ON referential_constraints.constraint_schema = key_column_usage.constraint_schema AND referential_constraints.constraint_name = key_column_usage.constraint_name AND referential_constraints.table_name = key_column_usage.table_name WHERE key_column_usage.constraint_schema = DATABASE() AND key_column_usage.table_name = ? AND key_column_usage.referenced_table_name IS NOT NULL ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).AddRow("id", "bigint", "NO", nil, nil, nil).AddRow("email", "varchar(255)", "NO", nil, nil, nil).AddRow("account_id", "bigint", "NO", nil, nil, nil))
	mock.ExpectQuery(primaryKeyQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery(uniqueQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "unsupported_index_metadata"}).AddRow("uq_users_email", "email", false, false, false, false, false, false))
	mock.ExpectQuery(checksQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}).AddRow("chk_users_email", "email <> ''", false, true, true))
	expectMySQLIndexesOnly(mock, "users")
	mock.ExpectQuery(foreignKeysQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_in_current_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}).AddRow("fk_users_account", "account_id", "accounts", "id", "c", "a", "s", true, false, false, false, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueConstraint{{Name: "uq_users_email", Columns: []string{"email"}}}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckConstraint{{Name: "chk_users_email", Expression: "email <> ''"}}, table.Checks)
	require.Equal(t, []schema.ForeignKey{{Name: "fk_users_account", Columns: []string{"account_id"}, ReferencedTable: "accounts", ReferencedColumns: []string{"id"}, OnDelete: schema.ReferenceActionCascade, OnUpdate: schema.ReferenceActionNoAction}}, table.ForeignKeys)

	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), "CONSTRAINT `uq_users_email` UNIQUE (`email`)")
	require.Contains(t, rendered.SQL(), "CONSTRAINT `chk_users_email` CHECK (email <> '')")
	require.Contains(t, rendered.SQL(), "CONSTRAINT `fk_users_account` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON DELETE CASCADE")
}

func TestMySQLInspectorReadsOrdinaryIndexes(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil).
			AddRow("email", "varchar(255)", "NO", nil, nil, nil))
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLEmptyMetadata(mock, "users")
	mock.ExpectQuery("SELECT index_name, 0, column_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' AND non_unique = 1 ORDER BY index_name, seq_in_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"index_name", "0", "column_name"}).AddRow("users_email_idx", false, "email"))
	mock.ExpectQuery("SELECT key_column_usage.constraint_name, key_column_usage.column_name, key_column_usage.referenced_table_name, key_column_usage.referenced_column_name, CASE referential_constraints.delete_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.delete_rule END, CASE referential_constraints.update_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.update_rule END, CASE referential_constraints.match_option WHEN 'NONE' THEN 's' ELSE referential_constraints.match_option END, key_column_usage.referenced_table_schema = DATABASE(), FALSE, FALSE, FALSE, TRUE, TRUE, FALSE FROM information_schema.key_column_usage JOIN information_schema.referential_constraints ON referential_constraints.constraint_schema = key_column_usage.constraint_schema AND referential_constraints.constraint_name = key_column_usage.constraint_name AND referential_constraints.table_name = key_column_usage.table_name WHERE key_column_usage.constraint_schema = DATABASE() AND key_column_usage.table_name = ? AND key_column_usage.referenced_table_name IS NOT NULL ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_in_current_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.Index{{Name: "users_email_idx", Columns: []string{"email"}}}, table.Indexes)
}

// TestMySQLInspectorRecordsUnsignedIntegerColumn follows one unsigned column
// the whole way: the catalog reports bigint(20) unsigned, the descriptor
// records it, the MySQL renderer puts the UNSIGNED back, and the generator
// emits a uint64 field for it. Before signedness reached schema.Column this
// same catalog row inspected into a plain integer column and re-rendered as
// `id` BIGINT, which stops at 9223372036854775807 and rejects every value a
// BIGINT UNSIGNED column above it holds.
func TestMySQLInspectorRecordsUnsignedIntegerColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint(20) unsigned", "NO", nil, int64(20), int64(0)).
			AddRow("sequence", "bigint", "NO", nil, int64(19), int64(0)))
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "events")

	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.IntegerType{Unsigned: true}},
		{Name: "sequence", Type: schema.IntegerType{}},
	}, table.Columns)

	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), "`id` BIGINT UNSIGNED NOT NULL")
	require.Contains(t, rendered.SQL(), "`sequence` BIGINT NOT NULL")

	source, err := generate.Schema("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+uint64$`, string(source))
	require.Regexp(t, `(?m)^\s*Sequence\s+int64$`, string(source))
}

// TestMySQLInspectorMatchesIntegerColumnTypeExactly covers the declarations
// that look like an integer without being one this package can represent. A
// substring test on "INT" accepted MySQL's own POINT, and it could not see the
// modifiers that follow the type at all.
func TestMySQLInspectorMatchesIntegerColumnTypeExactly(t *testing.T) {
	tests := map[string]struct {
		columnType string
		wantErr    string
	}{
		"integer as a substring": {
			columnType: "POINT",
			wantErr:    `unsupported mysql type "POINT"`,
		},
		"zerofill integer": {
			columnType: "bigint(20) unsigned zerofill",
			wantErr:    "must carry no UNSIGNED ZEROFILL modifier",
		},
		"signed zerofill integer": {
			columnType: "int(11) zerofill",
			wantErr:    "must carry no ZEROFILL modifier",
		},
		"malformed display width": {
			columnType: "bigint(twenty)",
			wantErr:    "an integer column must be declared BIGINT or BIGINT(width)",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			inspector, err := inspect.New(database, dialect.MySQL())
			require.NoError(t, err)
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
					AddRow("id", test.columnType, "NO", nil, int64(20), int64(0)))

			_, err = inspector.Table(t.Context(), "events")
			require.ErrorContains(t, err, test.wantErr)
			require.ErrorContains(t, err, `"id"`)
		})
	}
}

// TestMySQLInspectorAcceptsDocumentedIntegerSpellings is the positive
// counterpart: every integer spelling MySQL's catalog produces, with and
// without a display width, normalizes to schema.IntegerType carrying the
// signedness the declaration states. TINYINT(1) stays a boolean, and its
// unsigned form stays an integer, as both did before signedness was recorded.
func TestMySQLInspectorAcceptsDocumentedIntegerSpellings(t *testing.T) {
	tests := map[string]struct {
		columnType string
		want       schema.Column
	}{
		"bigint":                            {columnType: "bigint", want: schema.Column{Name: "id", Type: schema.IntegerType{}}},
		"bigint with width":                 {columnType: "bigint(20)", want: schema.Column{Name: "id", Type: schema.IntegerType{}}},
		"bigint unsigned":                   {columnType: "bigint unsigned", want: schema.Column{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		"bigint width unsigned":             {columnType: "bigint(20) unsigned", want: schema.Column{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		"int unsigned":                      {columnType: "int(10) unsigned", want: schema.Column{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		"integer alias":                     {columnType: "integer", want: schema.Column{Name: "id", Type: schema.IntegerType{}}},
		"smallint unsigned":                 {columnType: "smallint unsigned", want: schema.Column{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		"mediumint":                         {columnType: "mediumint", want: schema.Column{Name: "id", Type: schema.IntegerType{}}},
		"tinyint":                           {columnType: "tinyint", want: schema.Column{Name: "id", Type: schema.IntegerType{}}},
		"tinyint(1) is a boolean":           {columnType: "tinyint(1)", want: schema.Column{Name: "id", Type: schema.BooleanType{}}},
		"unsigned tinyint(1) is an integer": {columnType: "tinyint(1) unsigned", want: schema.Column{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			inspector, err := inspect.New(database, dialect.MySQL())
			require.NoError(t, err)
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
					AddRow("id", test.columnType, "NO", nil, int64(20), int64(0)))
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
			expectMySQLIndexes(mock, "events")

			table, err := inspector.Table(t.Context(), "events")
			require.NoError(t, err)
			require.Equal(t, []schema.Column{test.want}, table.Columns)
		})
	}
}

func TestMySQLInspectorNormalizesDecimalColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("amount", "decimal(10,2)", "NO", nil, int64(10), int64(2)))
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	expectMySQLIndexes(mock, "payments")

	table, err := inspector.Table(t.Context(), "payments")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}},
	}, table.Columns)
}

// TestMySQLInspectorMatchesDecimalColumnTypeExactly covers the two ways a
// MySQL COLUMN_TYPE can look like a decimal without being one this package can
// represent. The catalog is read from a server the application may not
// control, so a decimal is recognized from the whole declaration: catalog text
// that merely contains DECIMAL or NUMERIC is an unsupported type, and a real
// decimal declaration carrying a modifier is refused rather than silently
// re-rendered without it.
func TestMySQLInspectorMatchesDecimalColumnTypeExactly(t *testing.T) {
	tests := map[string]struct {
		columnType string
		wantErr    string
	}{
		"decimal as a substring": {
			columnType: "FOODECIMALBAR",
			wantErr:    `unsupported mysql type "FOODECIMALBAR"`,
		},
		"numeric as a substring": {
			columnType: "NOT_A_TYPE_NUMERICAL",
			wantErr:    `unsupported mysql type "NOT_A_TYPE_NUMERICAL"`,
		},
		"unsigned decimal": {
			columnType: "decimal(10,2) unsigned",
			wantErr:    "must carry no UNSIGNED modifier",
		},
		"zerofill decimal": {
			columnType: "decimal(10,2) unsigned zerofill",
			wantErr:    "must carry no UNSIGNED ZEROFILL modifier",
		},
		"decimal alias": {
			columnType: "fixed(10,2)",
			wantErr:    `unsupported mysql type "fixed(10,2)"`,
		},
		"malformed precision": {
			columnType: "decimal(ten,two)",
			wantErr:    "a decimal column must be declared DECIMAL(precision, scale)",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			inspector, err := inspect.New(database, dialect.MySQL())
			require.NoError(t, err)
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
					AddRow("amount", test.columnType, "NO", nil, int64(10), int64(2)))

			_, err = inspector.Table(t.Context(), "payments")
			require.ErrorContains(t, err, test.wantErr)
			require.ErrorContains(t, err, `"amount"`)
		})
	}
}

// TestMySQLInspectorAcceptsDocumentedDecimalSpellings is the positive
// counterpart: every spelling MySQL documents for a decimal type still
// normalizes to schema.DecimalType.
func TestMySQLInspectorAcceptsDocumentedDecimalSpellings(t *testing.T) {
	for _, columnType := range []string{"decimal", "decimal(10)", "decimal(10,2)", "numeric(10,2)", "DECIMAL(10, 2)"} {
		t.Run(columnType, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			inspector, err := inspect.New(database, dialect.MySQL())
			require.NoError(t, err)
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
					AddRow("amount", columnType, "NO", nil, int64(10), int64(2)))
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
			expectMySQLIndexes(mock, "payments")

			table, err := inspector.Table(t.Context(), "payments")
			require.NoError(t, err)
			require.Equal(t, []schema.Column{
				{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}},
			}, table.Columns)
		})
	}
}

// TestMySQLInspectorRejectsDecimalColumnWithoutScale is the MySQL counterpart
// of the PostgreSQL NULL-scale refusal.
func TestMySQLInspectorRejectsDecimalColumnWithoutScale(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("amount", "decimal(10,2)", "NO", nil, int64(10), nil))

	_, err = inspector.Table(t.Context(), "payments")
	require.ErrorContains(t, err, "reports no scale to record")
	require.ErrorContains(t, err, `"amount"`)
}

// TestSQLiteInspectorRejectsDecimalColumn pins the explicit-refusal property:
// a SQLite column declared DECIMAL/NUMERIC holds REAL values (see the
// dialect package's decimal type mapping), so inspection must keep refusing
// it rather than assert an exactness the stored data does not have, and must
// say why instead of falling through to the generic unsupported-type error.
func TestSQLiteInspectorRejectsDecimalColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list(\"payments\")").
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}).
			AddRow("main", "payments", "table", 1, 0, 0))
	mock.ExpectQuery("PRAGMA table_xinfo(\"payments\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "amount", "DECIMAL(19,4)", 1, nil, 0, 0))

	_, err = inspector.Table(t.Context(), "payments")
	require.ErrorContains(t, err, "is not exact in SQLite")
	require.ErrorContains(t, err, "NUMERIC-affinity")
	require.ErrorContains(t, err, `"amount"`)
}

func TestSQLiteInspectorUsesPragmaAndPrimaryKeyOrder(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list(\"events\")").
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}).
			AddRow("main", "events", "table", 3, 0, 0))
	mock.ExpectQuery("PRAGMA table_xinfo(\"events\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "sequence", "INTEGER", 1, nil, 2, 0).
			AddRow(1, "stream_id", "TEXT", 1, nil, 1, 0).
			AddRow(2, "payload", "BLOB", 0, nil, 0, 0))
	mock.ExpectQuery("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).
			AddRow("CREATE TABLE events (sequence INTEGER, stream_id TEXT, payload BLOB)"))
	mock.ExpectQuery(`PRAGMA index_list("events")`).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}))
	mock.ExpectQuery(`PRAGMA foreign_key_list("events")`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}))

	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []string{"stream_id", "sequence"}, table.PrimaryKey)
	require.Equal(t, schema.BytesType{}, table.Columns[2].Type)
	require.True(t, table.Columns[2].Nullable)
}

func TestSQLiteInspectorMarksIntegerPrimaryKeyAsNonNullable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events (id INTEGER PRIMARY KEY, payload BLOB)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)

	source, err := generate.Schema("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+int64$`, string(source))
	require.NotContains(t, string(source), "ID *int64")
}

func TestSQLiteInspectorUsesCanonicalTableNameForMixedCaseIdentifiers(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list(\"members\")").
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}).
			AddRow("main", "Members", "table", 1, 0, 0))
	mock.ExpectQuery("PRAGMA table_xinfo(\"Members\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "id", "INTEGER", 0, nil, 1, 0))
	mock.ExpectQuery("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("Members").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).
			AddRow("CREATE TABLE Members (id INTEGER PRIMARY KEY)"))
	mock.ExpectQuery(`PRAGMA index_list("Members")`).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}))
	mock.ExpectQuery(`PRAGMA foreign_key_list("Members")`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}))

	table, err := inspector.Table(t.Context(), "members")
	require.NoError(t, err)
	require.Equal(t, "Members", table.Name)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

func TestSQLiteInspectorReadsTableConstraints(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE parents (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id), email TEXT UNIQUE, CHECK (length(email) > 0))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "children")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueConstraint{{Columns: []string{"email"}}}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckConstraint{{Expression: "length(email) > 0"}}, table.Checks)
	require.Equal(t, []schema.ForeignKey{{
		Columns:           []string{"parent_id"},
		ReferencedTable:   "parents",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.ReferenceActionNoAction,
		OnUpdate:          schema.ReferenceActionNoAction,
	}}, table.ForeignKeys)
}

func TestSQLiteInspectorReadsConstraintsWithForeignKeyActions(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE parents (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE SET NULL, email TEXT UNIQUE, CHECK (parent_id > 0))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "children")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueConstraint{{Columns: []string{"email"}}}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckConstraint{{Expression: "parent_id > 0"}}, table.Checks)
	require.Equal(t, []schema.ForeignKey{{
		Columns:           []string{"parent_id"},
		ReferencedTable:   "parents",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.ReferenceActionCascade,
		OnUpdate:          schema.ReferenceActionSetNull,
	}}, table.ForeignKeys)
}

func TestSQLiteInspectorRejectsDeferrableForeignKeys(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE parents (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) DEFERRABLE INITIALLY DEFERRED)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = inspector.Table(t.Context(), "children")
	require.ErrorContains(t, err, "DEFERRABLE and INITIALLY foreign-key clauses are unsupported")
}

func TestSQLiteInspectorRejectsDescendingIndexes(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "database.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (parent_id INTEGER)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX children_parent_idx ON children (parent_id DESC)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = inspector.Table(t.Context(), "children")
	require.ErrorContains(t, err, "descending columns are unsupported")
}

func TestSQLiteInspectorRejectsUnrepresentableTableMetadata(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	for _, statement := range []string{
		"CREATE TABLE generated (value INTEGER, doubled INTEGER GENERATED ALWAYS AS (value * 2) STORED)",
		"CREATE TABLE autoincremented (id INTEGER PRIMARY KEY AUTOINCREMENT)",
		"CREATE TABLE strict_table (id INTEGER PRIMARY KEY) STRICT",
		"CREATE TABLE without_rowid (id INTEGER PRIMARY KEY) WITHOUT ROWID",
	} {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	for _, test := range []struct {
		table string
		want  string
	}{
		{table: "generated", want: "generated column"},
		{table: "autoincremented", want: "AUTOINCREMENT"},
		{table: "strict_table", want: "STRICT"},
		{table: "without_rowid", want: "WITHOUT ROWID"},
	} {
		t.Run(test.table, func(t *testing.T) {
			_, err := inspector.Table(t.Context(), test.table)
			require.ErrorContains(t, err, test.want)
		})
	}
}

// nilPointerDialect is a stub dialect.Dialect implemented with pointer
// receivers, so a typed-nil *nilPointerDialect value is a non-nil interface
// value that would panic if dereferenced.
type nilPointerDialect struct{}

func (*nilPointerDialect) Name() string { return "stub" }

func (*nilPointerDialect) QuoteIdentifier(string) (string, error) { return "", nil }

func (*nilPointerDialect) Placeholder(int) (string, error) { return "", nil }

func (*nilPointerDialect) TypeName(schema.Column) (string, error) { return "", nil }

func (*nilPointerDialect) UpsertStyle() dialect.UpsertStyle { return dialect.UpsertUnsupported }

func (*nilPointerDialect) Supports(dialect.Capability) bool { return false }

func TestNewRejectsTypedNilDependencies(t *testing.T) {
	_, err := inspect.New((*sql.DB)(nil), dialect.SQLite())
	require.ErrorContains(t, err, "queryer must not be nil")

	_, err = inspect.New(nil, dialect.SQLite())
	require.ErrorContains(t, err, "queryer must not be nil")

	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
	})
	_, err = inspect.New(database, (*nilPointerDialect)(nil))
	require.ErrorContains(t, err, "dialect must not be nil")

	var inspector inspect.Inspector
	require.NotPanics(t, func() {
		_, err = inspector.Table(t.Context(), "users")
	})
	require.ErrorContains(t, err, "invalid inspector")
}

func TestPostgreSQLInspectorReportsTableNotFoundWhenAbsent(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}))
	expectPostgreSQLCatalogColumnCountAbsent(mock, "widgets")

	_, err = inspector.Table(t.Context(), "widgets")
	require.Error(t, err)
	require.ErrorIs(t, err, inspect.ErrTableNotFound)
	var notFound *inspect.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, "widgets", notFound.Table)
	require.Contains(t, err.Error(), `"widgets"`)
	require.Contains(t, err.Error(), "current schema")
	require.NotContains(t, err.Error(), "normalize table")
}

// TestPostgreSQLInspectorReportsZeroColumnTableWhenPresent covers a table
// that genuinely has no user columns, which CREATE TABLE t () permits. This
// must stay distinguishable from both TableNotFoundError (the table exists)
// and IncompleteMetadataError (there is nothing hidden by privileges: the
// catalog agrees the column count is zero).
func TestPostgreSQLInspectorReportsZeroColumnTableWhenPresent(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 0)

	_, err = inspector.Table(t.Context(), "widgets")
	require.Error(t, err)
	var notFound *inspect.TableNotFoundError
	require.False(t, errors.As(err, &notFound))
	require.False(t, errors.Is(err, inspect.ErrTableNotFound))
	var incomplete *inspect.IncompleteMetadataError
	require.False(t, errors.As(err, &incomplete))
	require.False(t, errors.Is(err, inspect.ErrIncompleteMetadata))
	require.Contains(t, err.Error(), `"widgets"`)
	require.Contains(t, err.Error(), "zero-column")
}

// TestPostgreSQLInspectorReportsInvisibleColumnsWhenPresent covers the role
// that can see the table but none of its columns: information_schema.columns
// requires has_column_privilege, while information_schema.tables also accepts
// table-level privileges without column granularity such as DELETE, TRUNCATE
// and TRIGGER. pg_catalog still reports the real columns, so this must not be
// reported as a zero-column table.
func TestPostgreSQLInspectorReportsInvisibleColumnsWhenPresent(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 3)

	_, err = inspector.Table(t.Context(), "widgets")
	require.Error(t, err)
	require.ErrorIs(t, err, inspect.ErrIncompleteMetadata)
	var incomplete *inspect.IncompleteMetadataError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, "widgets", incomplete.Table)
	require.Equal(t, 0, incomplete.Visible)
	require.Equal(t, 3, incomplete.Actual)
	require.Contains(t, err.Error(), `"widgets"`)
	require.Contains(t, err.Error(), "column metadata could not be read")
	require.Contains(t, err.Error(), "3 columns")
	require.NotContains(t, err.Error(), "zero-column")
}

// TestPostgreSQLInspectorReportsTruncatedColumnsWhenPartiallyVisible covers
// defect 1: a role granted SELECT on only some columns of an existing table.
// information_schema.columns filters per column, so readColumns returns a
// short, nonempty list with no error of its own. Before this fix, the
// truncation check only ran when readColumns returned zero rows, so a
// nonzero-but-short result like this one produced a valid-looking descriptor
// with no error.
func TestPostgreSQLInspectorReportsTruncatedColumnsWhenPartiallyVisible(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil).
			AddRow("name", "character varying", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 3)

	table, err := inspector.Table(t.Context(), "widgets")
	require.Error(t, err)
	require.Equal(t, schema.Table{}, table)
	require.ErrorIs(t, err, inspect.ErrIncompleteMetadata)
	var incomplete *inspect.IncompleteMetadataError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, "widgets", incomplete.Table)
	require.Equal(t, 2, incomplete.Visible)
	require.Equal(t, 3, incomplete.Actual)
}

// TestPostgreSQLInspectorReturnsCompleteDescriptorWhenCountsAgree covers full
// visibility: readColumns and the pg_catalog count agree, so the inspector
// must proceed and return the complete descriptor rather than treat the
// agreement itself as suspicious.
func TestPostgreSQLInspectorReturnsCompleteDescriptorWhenCountsAgree(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil).
			AddRow("name", "character varying", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "widgets")

	table, err := inspector.Table(t.Context(), "widgets")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

// TestPostgreSQLInspectorReadsPrimaryKeyFromCatalogUnderReadOnlyGrant covers
// defect 2: a plain read-only role. information_schema.table_constraints
// deliberately omits SELECT from the privileges that expose a constraint, so
// a "GRANT SELECT" role sees every column but an empty primary key through
// information_schema. Before this fix that empty primary key passed
// validation (it is not a required field) and was returned with no error.
// pg_catalog.pg_constraint carries no such filter, so the primary key must
// now come back populated.
func TestPostgreSQLInspectorReadsPrimaryKeyFromCatalogUnderReadOnlyGrant(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("id", "bigint", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 1)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "widgets")

	table, err := inspector.Table(t.Context(), "widgets")
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

// TestPostgreSQLInspectorPreservesPrimaryKeyColumnOrder covers the ordering
// half of defect 2's fix: a primary key's column order is part of its
// identity, so switching the query source to pg_catalog must not scramble
// it. unnest(conkey) WITH ORDINALITY, ordered by that ordinal, is meant to
// preserve the same order key_column_usage.ordinal_position gave.
func TestPostgreSQLInspectorPreservesPrimaryKeyColumnOrder(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema\\.columns").
		WithArgs("memberships").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale"}).
			AddRow("tenant_id", "bigint", "NO", nil, nil, nil).
			AddRow("account_id", "bigint", "NO", nil, nil, nil).
			AddRow("user_id", "bigint", "NO", nil, nil, nil))
	expectPostgreSQLCatalogColumnCount(mock, "memberships", 3)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("memberships").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).
			AddRow("account_id").
			AddRow("tenant_id").
			AddRow("user_id"))
	expectPostgreSQLEmptyMetadata(mock, "memberships")

	table, err := inspector.Table(t.Context(), "memberships")
	require.NoError(t, err)
	require.Equal(t, []string{"account_id", "tenant_id", "user_id"}, table.PrimaryKey)
}

func expectPostgreSQLCatalogColumnCount(mock sqlmock.Sqlmock, tableName string, count int64) {
	mock.ExpectQuery("SELECT count\\(attribute\\.attnum\\) FROM pg_catalog\\.pg_class AS table_data.*JOIN pg_catalog\\.pg_namespace AS table_namespace.*LEFT JOIN pg_catalog\\.pg_attribute AS attribute.*table_data\\.relkind IN \\('r','p','v','f'\\) GROUP BY table_data\\.oid").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func expectPostgreSQLCatalogColumnCountAbsent(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT count\\(attribute\\.attnum\\) FROM pg_catalog\\.pg_class AS table_data.*JOIN pg_catalog\\.pg_namespace AS table_namespace.*LEFT JOIN pg_catalog\\.pg_attribute AS attribute.*table_data\\.relkind IN \\('r','p','v','f'\\) GROUP BY table_data\\.oid").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"count"}))
}

func TestSQLiteInspectorReportsTableNotFound(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list(\"ghosts\")").
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}))

	_, err = inspector.Table(t.Context(), "ghosts")
	require.Error(t, err)
	require.ErrorIs(t, err, inspect.ErrTableNotFound)
	var notFound *inspect.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, "ghosts", notFound.Table)
	require.Contains(t, err.Error(), `"ghosts"`)
	require.NotContains(t, err.Error(), "normalize table")
}
