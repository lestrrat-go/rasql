package inspect_test

import (
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
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
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil).
			AddRow("email", "character varying", "YES", driver.Value(nil)))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "users")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.Nil(t, table.UniqueConstraints)
	require.Nil(t, table.Checks)
	require.Nil(t, table.Indexes)
	require.Nil(t, table.ForeignKeys)
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
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil).
			AddRow("account_id", "bigint", "NO", nil).
			AddRow("tenant_id", "bigint", "NO", nil).
			AddRow("email", "character varying", "NO", nil))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct"}).
			AddRow("uq_users_email", "email", false, false, false).
			AddRow("uq_users_tenant_email", "tenant_id", false, false, false).
			AddRow("uq_users_tenant_email", "email", false, false, false))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit"}).
			AddRow("chk_users_email", "email <> ''", false))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*index_metadata\\.indnullsnotdistinct.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname"}).
			AddRow("users_email_idx", false, "email").
			AddRow("users_tenant_email_idx", true, "tenant_id").
			AddRow("users_tenant_email_idx", true, "email"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "c", "a", "s", true, false, false, false).
			AddRow("fk_users_account", "tenant_id", "accounts", "tenant_id", "c", "a", "s", true, false, false, false))

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

func TestPostgreSQLInspectorRejectsUnsupportedIndex(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*index_metadata\\.indnullsnotdistinct.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("users_email_partial_idx"))

	_, err = inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: index \"users_email_partial_idx\" cannot be represented: rasql supports only non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, and distinct nulls")
}

func TestPostgreSQLInspectorRejectsUnsupportedUniqueConstraint(t *testing.T) {
	tests := []struct {
		name              string
		deferrable        bool
		initiallyDeferred bool
		nullsNotDistinct  bool
	}{
		{name: "deferrable", deferrable: true},
		{name: "initially deferred", deferrable: true, initiallyDeferred: true},
		{name: "nulls not distinct", nullsNotDistinct: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
			mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct"}).
					AddRow("uq_users_email", "email", test.deferrable, test.initiallyDeferred, test.nullsNotDistinct))

			_, err := inspector.Table(t.Context(), "users")
			require.EqualError(t, err, "inspect: unique constraint \"uq_users_email\" cannot be represented: rasql supports only non-deferrable unique constraints with distinct nulls")
		})
	}
}

func TestPostgreSQLInspectorRejectsNoInheritCheck(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit"}).
			AddRow("chk_users_email", "email <> ''", true))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, "inspect: check constraint \"chk_users_email\" cannot be represented: rasql does not support NO INHERIT check constraints")
}

func TestPostgreSQLInspectorRejectsUnsupportedForeignKey(t *testing.T) {
	tests := []struct {
		name              string
		matchType         string
		inCurrentSchema   bool
		deferrable        bool
		initiallyDeferred bool
		deleteSetColumns  bool
		want              string
	}{
		{name: "match full", matchType: "f", inCurrentSchema: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports only MATCH SIMPLE foreign keys"},
		{name: "referenced table outside current schema", matchType: "s", want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports references only in the current schema"},
		{name: "deferrable", matchType: "s", inCurrentSchema: true, deferrable: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports only non-deferrable foreign keys"},
		{name: "initially deferred", matchType: "s", inCurrentSchema: true, deferrable: true, initiallyDeferred: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql supports only non-deferrable foreign keys"},
		{name: "partial delete set columns", matchType: "s", inCurrentSchema: true, deleteSetColumns: true, want: "inspect: foreign key \"fk_users_account\" cannot be represented: rasql does not support column lists for ON DELETE SET NULL or SET DEFAULT"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
			expectPostgreSQLMetadataBeforeForeignKeys(mock, "users")
			mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL FROM pg_catalog\\.pg_constraint").
				WithArgs("users").
				WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns"}).
					AddRow("fk_users_account", "account_id", "accounts", "id", "a", "a", test.matchType, test.inCurrentSchema, test.deferrable, test.initiallyDeferred, test.deleteSetColumns))

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
	mock.ExpectQuery("SELECT column_name, data_type, is_nullable, column_default FROM information_schema\\.columns").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil))
	mock.ExpectQuery("SELECT key_column_usage\\.column_name FROM information_schema\\.table_constraints").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
}

func expectPostgreSQLEmptyMetadata(mock sqlmock.Sqlmock, tableName string) {
	expectPostgreSQLMetadataBeforeForeignKeys(mock, tableName)
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, referenced_namespace\\.nspname = current_schema\\(\\), constraint_data\\.condeferrable, constraint_data\\.condeferred, constraint_data\\.confdelsetcols IS NOT NULL FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_in_current_schema", "condeferrable", "condeferred", "delete_set_columns"}))
}

func expectPostgreSQLMetadataBeforeForeignKeys(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit"}))
	mock.ExpectQuery("SELECT index_data\\.relname FROM pg_catalog\\.pg_index.*index_metadata\\.indnullsnotdistinct.*operator_class_metadata\\.opcdefault.*index_collation\\.collation_oid <> attribute\\.attcollation").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname FROM pg_catalog\\.pg_index").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"relname", "indisunique", "attname"}))
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
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default"}).
			AddRow("id", "bigint", "NO", nil).
			AddRow("active", "tinyint(1)", "NO", nil).
			AddRow("login_attempts", "tinyint", "NO", nil))
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "active", Type: schema.TypeBoolean},
		{Name: "login_attempts", Type: schema.TypeInteger},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)

	source, err := generate.Schema("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Active\s+bool$`, string(source))
	require.Regexp(t, `(?m)^\s*LoginAttempts\s+int64$`, string(source))
	require.Contains(t, string(source), `row.Assign(src, "active", &r.Active)`)
	require.Contains(t, string(source), `row.Assign(src, "login_attempts", &r.LoginAttempts)`)
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
	mock.ExpectQuery("PRAGMA table_info(\"events\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk"}).
			AddRow(0, "sequence", "INTEGER", 1, nil, 2).
			AddRow(1, "stream_id", "TEXT", 1, nil, 1).
			AddRow(2, "payload", "BLOB", 0, nil, 0))

	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []string{"stream_id", "sequence"}, table.PrimaryKey)
	require.Equal(t, schema.TypeBytes, table.Columns[2].Type)
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
		{Name: "id", Type: schema.TypeInteger},
		{Name: "payload", Type: schema.TypeBytes, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)

	source, err := generate.Schema("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+int64$`, string(source))
	require.NotContains(t, string(source), "ID *int64")
}
