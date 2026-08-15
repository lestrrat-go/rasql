package inspect_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/migrate/diff"
	mysqldiff "github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "YES", driver.Value(nil), nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "users")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.Nil(t, table.UniqueConstraints)
	require.Nil(t, table.Checks)
	require.Nil(t, table.Indexes)
	require.Nil(t, table.ForeignKeys)
}

// TestPostgreSQLInspectorRecordsGeneratedColumns covers both PostgreSQL
// generated column storage kinds: STORED, which every supported PostgreSQL
// version records, and VIRTUAL, which pg_catalog.pg_attribute.attgenerated
// only ever reports as 'v' from PostgreSQL 18 onward. Before this feature
// existed, the PostgreSQL columns query selected no generated-column
// metadata at all, so a generated column inspected as an ordinary column
// with an empty default rather than being rejected or flagged -- the exact
// silent mislabeling this test proves no longer happens.
// information_schema.columns.is_generated is the authoritative signal that
// a column is generated at all; generation_expression supplies the
// expression text; pg_attribute.attgenerated, joined into the same query,
// supplies the storage kind information_schema itself does not carry.
// render.CreateTable still refuses to build DDL for a generated column, see
// TestCreateTableRejectsGeneratedColumn in the render package.
func TestPostgreSQLInspectorRecordsGeneratedColumns(t *testing.T) {
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("measurements").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("celsius", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("fahrenheit_stored", "bigint", "NO", nil, nil, nil, nil, "ALWAYS", "celsius * 9 / 5 + 32", "s").
			AddRow("fahrenheit_virtual", "bigint", "NO", nil, nil, nil, nil, "ALWAYS", "celsius * 9 / 5 + 32", "v"))
	expectPostgreSQLCatalogColumnCount(mock, "measurements", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("measurements").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "measurements")

	table, err := inspector.Table(t.Context(), "measurements")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "celsius", Type: schema.IntegerType{}},
		{
			Name:                "fahrenheit_stored",
			Type:                schema.IntegerType{},
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedStored,
		},
		{
			Name:                "fahrenheit_virtual",
			Type:                schema.IntegerType{},
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedVirtual,
		},
	}, table.Columns)
	require.NoError(t, table.Validate())

	_, err = render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"fahrenheit_stored"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestPostgreSQLInspectorNormalizesTextWidth covers schema.TextType.Width
// preservation for PostgreSQL, the counterpart to
// TestMySQLInspectorNormalizesTextWidth: VARCHAR(n) and CHARACTER(n) round-trip
// their stated width from character_maximum_length, the only place PostgreSQL
// carries one, since data_type never spells a length the way MySQL's
// column_type does. An unbounded CHARACTER VARYING and bare TEXT report a
// NULL character_maximum_length and normalize to an unstated width, not a
// stated width of 0. data_type also distinguishes character from character
// varying, so a CHARACTER(n) column normalizes with Fixed set, matching how
// MySQL's CHAR is handled, and re-renders as CHAR(n) rather than VARCHAR(n).
func TestPostgreSQLInspectorNormalizesTextWidth(t *testing.T) {
	tests := map[string]struct {
		dataType               string
		characterMaximumLength any
		want                   schema.ColumnType
	}{
		"varchar with width":          {dataType: "character varying", characterMaximumLength: int64(255), want: schema.TextType{Width: schema.NewTextWidth(255)}},
		"character with width":        {dataType: "character", characterMaximumLength: int64(36), want: schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}},
		"unbounded character varying": {dataType: "character varying", characterMaximumLength: nil, want: schema.TextType{}},
		"text":                        {dataType: "text", characterMaximumLength: nil, want: schema.TextType{}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			inspector, mock := newPostgreSQLInspector(t)
			expectPostgreSQLServerVersion(mock, "180000")
			mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
					AddRow("value", test.dataType, "NO", nil, nil, nil, test.characterMaximumLength, "NEVER", nil, ""))
			expectPostgreSQLCatalogColumnCount(mock, "events", 1)
			mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"attname"}))
			expectPostgreSQLEmptyMetadata(mock, "events")

			table, err := inspector.Table(t.Context(), "events")
			require.NoError(t, err)
			require.Equal(t, []schema.ColumnDef{{Name: "value", Type: test.want}}, table.Columns)
		})
	}
}

// TestPostgreSQLInspectorRoundTripsTextWidthWithoutSpuriousDiff is a
// regression test for the defect this package's PostgreSQL width support
// fixes: before character_maximum_length was read, a live VARCHAR(255)
// column inspected back with an unstated width, so migrate/diff/postgresql
// compared the desired schema's stated VARCHAR(255) against a re-rendered
// TEXT and refused with "requires manual migration" instead of the empty
// plan an unchanged column deserves. This exercises the same
// render.CreateTable -> inspect -> LiveSources -> Diff path diff-live uses.
func TestPostgreSQLInspectorRoundTripsTextWidthWithoutSpuriousDiff(t *testing.T) {
	desired := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}},
		},
		PrimaryKey: []string{"id"},
	}
	createTable, err := render.CreateTable(dialect.PostgreSQL(), desired)
	require.NoError(t, err)

	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "NO", nil, nil, nil, int64(255), "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "users")

	live, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(255)}, live.Columns[1].Type)

	analyzer := postgresql.New()
	baseline, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: createTable.SQL()}})
	require.NoError(t, err)
	liveSources, err := analyzer.LiveSources(live)
	require.NoError(t, err)
	liveSnapshot, err := analyzer.Parse(liveSources)
	require.NoError(t, err)

	plan, err := analyzer.Diff(baseline, liveSnapshot)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

// TestPostgreSQLInspectorRoundTripsCharacterWidthWithoutSpuriousDiff covers
// PostgreSQL's CHARACTER(n), the counterpart to
// TestPostgreSQLInspectorRoundTripsTextWidthWithoutSpuriousDiff: before
// data_type distinguished character from character varying, a live
// CHARACTER(n) column inspected with an unstated fixed-ness and re-rendered
// as VARCHAR(n), the CHARACTER(n) limitation this package's PostgreSQL
// Fixed support closes.
func TestPostgreSQLInspectorRoundTripsCharacterWidthWithoutSpuriousDiff(t *testing.T) {
	desired := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "code", Type: schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}},
		},
		PrimaryKey: []string{"id"},
	}
	createTable, err := render.CreateTable(dialect.PostgreSQL(), desired)
	require.NoError(t, err)

	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("code", "character", "NO", nil, nil, nil, int64(10), "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "users")

	live, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}, live.Columns[1].Type)

	analyzer := postgresql.New()
	baseline, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: createTable.SQL()}})
	require.NoError(t, err)
	liveSources, err := analyzer.LiveSources(live)
	require.NoError(t, err)
	liveSnapshot, err := analyzer.Parse(liveSources)
	require.NoError(t, err)

	plan, err := analyzer.Diff(baseline, liveSnapshot)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("amount", "numeric", "NO", nil, int64(19), int64(4), nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "payments", 1)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}))
	expectPostgreSQLEmptyMetadata(mock, "payments")

	table, err := inspector.Table(t.Context(), "payments")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("amount", "numeric", "NO", nil, nil, nil, nil, "NEVER", nil, ""))

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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("amount", "numeric", "NO", nil, int64(10), nil, nil, "NEVER", nil, ""))

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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("account_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("tenant_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}).
			AddRow("uq_users_email", "email", false, false, false, nil, false, nil, nil, false, nil).
			AddRow("uq_users_tenant_email", "tenant_id", false, false, false, nil, false, nil, nil, false, nil).
			AddRow("uq_users_tenant_email", "email", false, false, false, nil, false, nil, nil, false, nil))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}).
			AddRow("chk_users_email", "email <> ''", false, true, true))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_email_idx", false, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_tenant_email_idx", true, "tenant_id", "tenant_id", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_tenant_email_idx", true, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false))
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, \\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\), constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "c", "a", "s", "", false, false, nil, true, true, false).
			AddRow("fk_users_account", "tenant_id", "accounts", "tenant_id", "c", "a", "s", "", false, false, nil, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "uq_users_email", Columns: []string{"email"}},
		{Name: "uq_users_tenant_email", Columns: []string{"tenant_id", "email"}},
	}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckDef{
		{Name: "chk_users_email", Expression: "email <> ''"},
	}, table.Checks)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_idx", Columns: []string{"email"}},
		{Name: "users_tenant_email_idx", Columns: []string{"tenant_id", "email"}, Unique: true},
	}, table.Indexes)
	require.Equal(t, []schema.ForeignKeyDef{
		{
			Name:              "fk_users_account",
			Columns:           []string{"account_id", "tenant_id"},
			ReferencedTable:   "accounts",
			ReferencedColumns: []string{"id", "tenant_id"},
			OnDelete:          schema.Cascade,
			OnUpdate:          schema.NoAction,
		},
	}, table.ForeignKeys)

	source, err := generate.DescriptorSource("generated", table)
	require.NoError(t, err)
	require.Contains(t, string(source), `{Name: "uq_users_email", Columns: []string{"email"}}`)
	require.Contains(t, string(source), `{Name: "uq_users_tenant_email", Columns: []string{"tenant_id", "email"}}`)
	require.Contains(t, string(source), `{Name: "chk_users_email", Expression: "email <> ''"}`)
	require.Contains(t, string(source), `{Name: "users_email_idx", Columns: []string{"email"}}`)
	require.Contains(t, string(source), `{Name: "users_tenant_email_idx", Columns: []string{"tenant_id", "email"}, Unique: true}`)
	require.Contains(t, string(source), `{Name: "fk_users_account", Columns: []string{"account_id", "tenant_id"}, ReferencedTable: "accounts", ReferencedColumns: []string{"id", "tenant_id"}, OnDelete: schema.Cascade, OnUpdate: schema.NoAction}`)
	require.Contains(t, string(source), `{Name: "Account", Kind: schema.RelationshipBelongsTo, Columns: []string{"account_id", "tenant_id"}, ReferencedTable: "accounts", ReferencedColumns: []string{"id", "tenant_id"}}`)
}

// TestPostgreSQLInspectorRecordsNonDefaultIndexMethod proves that a
// PostgreSQL index built with a non-btree access method, such as GIN, is now
// described rather than rejected: inspect used to fail the whole table on
// the first such index, which would abort a sweep over a production schema
// the moment it reached one GIN index. The plain btree index alongside it
// keeps schema.IndexMethod's zero value, proving the field only records a
// method when it differs from the default.
func TestPostgreSQLInspectorRecordsNonDefaultIndexMethod(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_id_idx", false, "id", "id", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_id_gin_idx", false, "id", "id", "gin", nil, false, nil, nil, nil, false, nil, nil, false, false, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_id_idx", Columns: []string{"id"}},
		{Name: "users_id_gin_idx", Columns: []string{"id"}, Method: "gin"},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsPartialIndex proves that a PostgreSQL index
// with a WHERE predicate is now described rather than rejected: inspect
// used to fail the whole table on the first partial index, which would
// abort a sweep over a production schema the moment it reached one. The
// plain index alongside it keeps IndexDef.Predicate's zero value, proving
// the field only records a predicate when the index actually has one.
func TestPostgreSQLInspectorRecordsPartialIndex(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_id_idx", false, "id", "id", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_active_id_idx", false, "id", "id", "btree", "id > 0", false, nil, nil, nil, false, nil, nil, false, false, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_id_idx", Columns: []string{"id"}},
		{Name: "users_active_id_idx", Columns: []string{"id"}, Predicate: "id > 0"},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsExpressionIndex proves that a PostgreSQL
// expression index, and one that mixes a plain column with an expression,
// are now described rather than rejected. The mixed index proves
// Expressions carries every key including the plain one, verbatim as its
// bare column name, and leaves Columns empty rather than splitting the key
// order across both fields.
func TestPostgreSQLInspectorRecordsExpressionIndex(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_lower_email_idx", false, nil, "lower(email)", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_id_lower_email_idx", false, "id", "id", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_id_lower_email_idx", false, nil, "lower(email)", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_lower_email_idx", Expressions: []string{"lower(email)"}},
		{Name: "users_id_lower_email_idx", Expressions: []string{"id", "lower(email)"}},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsIndexValidityStorageAndPlacement proves that
// an invalid index, one with storage parameters, one on a nondefault
// tablespace, and one marking the table's replica identity are now
// described rather than rejected: inspect used to fail the whole table on
// the first index carrying any of these, which would abort a sweep over a
// production schema the moment it reached one. The plain index alongside
// them keeps every new field's zero value, proving each one only records a
// fact when the index actually has it. The replica identity index is also
// unique, since PostgreSQL requires REPLICA IDENTITY USING INDEX to name a
// unique index and schema.TableDef.Validate now rejects the same
// combination.
func TestPostgreSQLInspectorRecordsIndexValidityStorageAndPlacement(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_email_idx", false, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, false).
			AddRow("users_email_invalid_idx", false, "email", "email", "btree", nil, false, nil, nil, nil, true, nil, nil, false, false, false).
			AddRow("users_email_options_idx", false, "email", "email", "btree", nil, false, nil, nil, nil, false, "fillfactor=70", nil, false, false, false).
			AddRow("users_email_tablespace_idx", false, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, "pg_custom", false, false, false).
			AddRow("users_email_replident_idx", true, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, nil, true, false, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_idx", Columns: []string{"email"}},
		{Name: "users_email_invalid_idx", Columns: []string{"email"}, NotValid: true},
		{Name: "users_email_options_idx", Columns: []string{"email"}, StorageParameters: map[string]string{"fillfactor": "70"}},
		{Name: "users_email_tablespace_idx", Columns: []string{"email"}, Tablespace: "pg_custom"},
		{Name: "users_email_replident_idx", Columns: []string{"email"}, Unique: true, ReplicaIdentity: true},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsIndexNullsFacts proves that a plain
// (non-constraint) unique index declared NULLS NOT DISTINCT and a key whose
// NULLS FIRST/NULLS LAST placement overrides the default its own ASC/DESC
// direction implies are now described rather than rejected: inspect used to
// fail the whole table on the first index carrying either, which would abort
// a sweep over a production schema the moment it reached one. The nulls-
// first key forces the index onto Keys, the same way any other per-key fact
// does; the plain-column index carries IndexDef.NullsNotDistinct alone.
func TestPostgreSQLInspectorRecordsIndexNullsFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_email_nulls_first_idx", false, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, nil, false, false, true).
			AddRow("users_email_nulls_not_distinct_idx", true, "email", "email", "btree", nil, false, nil, nil, nil, false, nil, nil, false, true, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_nulls_first_idx", Keys: []schema.IndexKeyDef{{Expression: "email", NullsOrder: schema.NullsFirst}}},
		{Name: "users_email_nulls_not_distinct_idx", Columns: []string{"email"}, Unique: true, NullsNotDistinct: true},
	}, table.Indexes)
}

// TestPostgreSQLInspectorRecordsIndexKeyDetails proves that a descending
// key, a non-default per-key collation, a non-default operator class, and
// an index's INCLUDE columns are now described rather than rejected:
// inspect used to fail the whole table on the first index carrying any of
// these, which would abort a sweep over a production schema the moment it
// reached one. Each index below carries exactly one of the four facts,
// proving IndexDef.Keys and IndexDef.IncludeColumns only record a fact when
// the index actually has it, and that a plain-column index with no per-key
// fact still describes its keys with Columns alone.
func TestPostgreSQLInspectorRecordsIndexKeyDetails(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("bio", "text", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("name", "text", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("status", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns).
			AddRow("users_id_desc_idx", false, "id", "id", "btree", nil, true, nil, nil, nil, false, nil, nil, false, false, true).
			AddRow("users_bio_opclass_idx", false, "bio", "bio", "btree", nil, false, "text_pattern_ops", nil, nil, false, nil, nil, false, false, false).
			AddRow("users_name_collation_idx", false, "name", "name", "btree", nil, false, nil, "C", nil, false, nil, nil, false, false, false).
			AddRow("users_id_status_idx", false, "id", "id", "btree", nil, false, nil, nil, "status", false, nil, nil, false, false, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_id_desc_idx", Keys: []schema.IndexKeyDef{{Expression: "id", Descending: true}}},
		{Name: "users_bio_opclass_idx", Keys: []schema.IndexKeyDef{{Expression: "bio", OperatorClass: "text_pattern_ops"}}},
		{Name: "users_name_collation_idx", Keys: []schema.IndexKeyDef{{Expression: "name", Collation: "C"}}},
		{Name: "users_id_status_idx", Columns: []string{"id"}, IncludeColumns: []string{"status"}},
	}, table.Indexes)
}

func TestPostgreSQLInspectorUsesPostgreSQL14CatalogQueries(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "140000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	expectPostgreSQLMetadataBeforeForeignKeysWithChecks(mock, "users", "140000", sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}).
		AddRow("chk_users_email", "email <> ''", false, true, true))
	expectPostgreSQLForeignKeysWithRows(mock, "users", "140000", sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
		AddRow("fk_users_account", "id", "accounts", "id", "a", "a", "s", "", false, false, nil, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.CheckDef{{Name: "chk_users_email", Expression: "email <> ''"}}, table.Checks)
	require.Equal(t, []schema.ForeignKeyDef{{
		Name:              "fk_users_account",
		Columns:           []string{"id"},
		ReferencedTable:   "accounts",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.NoAction,
		OnUpdate:          schema.NoAction,
	}}, table.ForeignKeys)
}

// TestPostgreSQLInspectorRecordsUniqueConstraintBackingIndexFacts proves that
// a temporal unique constraint and a unique constraint whose backing index
// carries storage parameters, a nondefault tablespace, a nondefault column
// collation, or the table's replica identity are now described rather than
// rejected: inspect used to fail the whole table on any of these, which
// would abort a sweep over a production schema the moment it reached one
// such unique constraint.
func TestPostgreSQLInspectorRecordsUniqueConstraintBackingIndexFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("status", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("slug", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("code", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 5)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}).
			AddRow("uq_users_email", "email", false, false, false, nil, true, nil, nil, false, nil).
			AddRow("uq_users_status", "status", false, false, false, nil, false, "fillfactor=70", nil, false, nil).
			AddRow("uq_users_slug", "slug", false, false, false, nil, false, nil, "pg_custom", false, "C").
			AddRow("uq_users_code", "code", false, false, false, nil, false, nil, nil, true, nil))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "uq_users_email", Columns: []string{"email"}, Temporal: true},
		{Name: "uq_users_status", Columns: []string{"status"}, StorageParameters: map[string]string{"fillfactor": "70"}},
		{Name: "uq_users_slug", Columns: []string{"slug"}, Tablespace: "pg_custom", Collations: map[string]string{"slug": "C"}},
		{Name: "uq_users_code", Columns: []string{"code"}, ReplicaIdentity: true},
	}, table.UniqueConstraints)
}

// TestPostgreSQLInspectorRecordsUniqueConstraintFacts proves that a
// deferrable unique constraint, a NULLS NOT DISTINCT unique constraint, and
// a unique constraint with INCLUDE columns are now described rather than
// rejected: inspect used to fail the whole table on any of these, which
// would abort a sweep over a production schema the moment it reached one
// such unique constraint.
func TestPostgreSQLInspectorRecordsUniqueConstraintFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("email", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("status", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("slug", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, index_metadata\\.indnullsnotdistinct, .*, constraint_data\\.conperiod, array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}).
			AddRow("uq_users_email", "email", true, false, false, nil, false, nil, nil, false, nil).
			AddRow("uq_users_status", "status", false, false, true, nil, false, nil, nil, false, nil).
			AddRow("uq_users_slug", "slug", false, false, false, "email,status", false, nil, nil, false, nil))
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, constraint_data\\.conenforced FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}))
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, index_metadata\\.indnullsnotdistinct, \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Name: "uq_users_email", Columns: []string{"email"}, Deferrable: schema.DeferrableInitiallyImmediate},
		{Name: "uq_users_status", Columns: []string{"status"}, NullsNotDistinct: true},
		{Name: "uq_users_slug", Columns: []string{"slug"}, IncludeColumns: []string{"email", "status"}},
	}, table.UniqueConstraints)
}

// TestPostgreSQLInspectorRecordsCheckFacts proves that a check constraint
// declared NO INHERIT, NOT VALID, or NOT ENFORCED is described rather than
// rejected: inspect used to fail the whole table on any of these, which
// would abort a sweep over a production schema the moment it reached one
// such check constraint.
func TestPostgreSQLInspectorRecordsCheckFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	expectPostgreSQLMetadataBeforeForeignKeysWithChecks(mock, "users", "180000", sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}).
		AddRow("chk_users_default", "email <> ''", false, true, true).
		AddRow("chk_users_no_inherit", "age >= 0", true, true, true).
		AddRow("chk_users_not_valid", "age < 150", false, false, true).
		AddRow("chk_users_not_enforced", "id > 0", false, true, false))
	expectPostgreSQLForeignKeys(mock, "users", "180000")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.CheckDef{
		{Name: "chk_users_default", Expression: "email <> ''"},
		{Name: "chk_users_no_inherit", Expression: "age >= 0", NoInherit: true},
		{Name: "chk_users_not_valid", Expression: "age < 150", NotValid: true},
		{Name: "chk_users_not_enforced", Expression: "id > 0", NotEnforced: true},
	}, table.Checks)
}

// TestPostgreSQLInspectorRecordsForeignKeyTemporalAndDeleteSetColumns proves
// that a foreign key's ON DELETE SET NULL column list and a temporal
// (PERIOD) foreign key are now described rather than rejected: inspect used
// to fail the whole table on either, which would abort a sweep over a
// production schema the moment it reached one such foreign key.
func TestPostgreSQLInspectorRecordsForeignKeyTemporalAndDeleteSetColumns(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("account_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("period_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 3)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLMetadataBeforeForeignKeys(mock, "users", "180000")
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, \\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\), constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "n", "a", "s", "", false, false, "account_id", true, true, false).
			AddRow("fk_users_period", "period_id", "periods", "id", "a", "a", "s", "", false, false, nil, true, true, true))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{
		{Name: "fk_users_account", Columns: []string{"account_id"}, ReferencedTable: "accounts", ReferencedColumns: []string{"id"}, OnDelete: schema.SetNull, OnUpdate: schema.NoAction, DeleteSetColumns: []string{"account_id"}},
		{Name: "fk_users_period", Columns: []string{"period_id"}, ReferencedTable: "periods", ReferencedColumns: []string{"id"}, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, Temporal: true},
	}, table.ForeignKeys)
}

// TestPostgreSQLInspectorRejectsUnsupportedForeignKeyMatchType proves that
// an unrecognized MATCH code (anything other than PostgreSQL's own "s",
// "f", or "p") is still rejected, distinct from a MATCH FULL or MATCH
// PARTIAL foreign key, which TestPostgreSQLInspectorRecordsForeignKeyFacts
// proves inspect now describes.
func TestPostgreSQLInspectorRejectsUnsupportedForeignKeyMatchType(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	expectPostgreSQLColumnsAndPrimaryKey(mock, "users")
	expectPostgreSQLMetadataBeforeForeignKeys(mock, "users", "180000")
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, \\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\), constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "a", "a", "x", "", false, false, nil, true, true, false))

	_, err := inspector.Table(t.Context(), "users")
	require.EqualError(t, err, `inspect: foreign key "fk_users_account": unsupported match type "x"`)
}

// TestPostgreSQLInspectorRecordsForeignKeyFacts proves that a foreign key
// referencing another schema, a deferrable foreign key, and a foreign key
// with a MATCH FULL or MATCH PARTIAL clause are now described rather than
// rejected: inspect used to fail the whole table on any of these, which
// would abort a sweep over a production schema the moment it reached one
// such foreign key.
func TestPostgreSQLInspectorRecordsForeignKeyFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("account_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("owner_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("group_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLMetadataBeforeForeignKeys(mock, "users", "180000")
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, \\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\), constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "a", "a", "s", "billing", false, false, nil, true, true, false).
			AddRow("fk_users_owner", "owner_id", "owners", "id", "a", "a", "f", "", true, false, nil, true, true, false).
			AddRow("fk_users_group", "group_id", "groups", "id", "a", "a", "p", "", true, true, nil, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{
		{Name: "fk_users_account", Columns: []string{"account_id"}, ReferencedSchema: "billing", ReferencedTable: "accounts", ReferencedColumns: []string{"id"}, OnDelete: schema.NoAction, OnUpdate: schema.NoAction},
		{Name: "fk_users_owner", Columns: []string{"owner_id"}, ReferencedTable: "owners", ReferencedColumns: []string{"id"}, Match: schema.MatchFull, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, Deferrable: schema.DeferrableInitiallyImmediate},
		{Name: "fk_users_group", Columns: []string{"group_id"}, ReferencedTable: "groups", ReferencedColumns: []string{"id"}, Match: schema.MatchPartial, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, Deferrable: schema.DeferrableInitiallyDeferred},
	}, table.ForeignKeys)
}

// TestPostgreSQLInspectorRecordsExclusionConstraintFacts proves that a
// PostgreSQL EXCLUDE constraint is described instead of rejected: inspect
// used to fail the whole table the moment it found any exclusion
// constraint, which would abort a sweep over a production schema on its
// first one. reservations_gist_excl pins that a non-default access method
// is recorded in Method and that an omitted predicate and deferrability
// decode to their zero values; reservations_no_double_booking pins a
// multi-element constraint with a partial predicate and DEFERRABLE
// INITIALLY DEFERRED, assembled from several rows the same way
// TestPostgreSQLInspectorRecordsForeignKeyFacts assembles a composite
// foreign key.
func TestPostgreSQLInspectorRecordsExclusionConstraintFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("reservations").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("room", "text", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("party_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("active", "boolean", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "reservations", 4)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("reservations").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	exclusionRows := sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}).
		AddRow("reservations_gist_excl", "gist", "room", "=", nil, false, false).
		AddRow("reservations_no_double_booking", "btree", "room", "=", "active", true, true).
		AddRow("reservations_no_double_booking", "btree", "party_id", "=", "active", true, true)
	expectPostgreSQLMetadataBeforeForeignKeysWithChecksAndExclusions(mock, "reservations", "180000", sqlmock.NewRows([]string{"conname", "expression", "connoinherit", "convalidated", "conenforced"}), exclusionRows)
	expectPostgreSQLForeignKeys(mock, "reservations", "180000")

	table, err := inspector.Table(t.Context(), "reservations")
	require.NoError(t, err)
	require.Equal(t, []schema.ExclusionDef{
		{
			Name:     "reservations_gist_excl",
			Method:   "gist",
			Elements: []schema.ExclusionElementDef{{Expression: "room", Operator: "="}},
		},
		{
			Name: "reservations_no_double_booking",
			Elements: []schema.ExclusionElementDef{
				{Expression: "room", Operator: "="},
				{Expression: "party_id", Operator: "="},
			},
			Predicate:  "active",
			Deferrable: schema.DeferrableInitiallyDeferred,
		},
	}, table.ExclusionConstraints)
}

// TestPostgreSQLInspectorRecordsForeignKeyValidationFacts proves that a
// foreign key declared NOT VALID or NOT ENFORCED is described rather than
// rejected: inspect used to fail the whole table on either, which would
// abort a sweep over a production schema the moment it reached one such
// foreign key.
func TestPostgreSQLInspectorRecordsForeignKeyValidationFacts(t *testing.T) {
	inspector, mock := newPostgreSQLInspector(t)
	expectPostgreSQLServerVersion(mock, "180000")
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("account_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("owner_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "users", 3)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'p'").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLMetadataBeforeForeignKeys(mock, "users", "180000")
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, \\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\), constraint_data\\.convalidated, constraint_data\\.conenforced, constraint_data\\.conperiod FROM pg_catalog\\.pg_constraint").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}).
			AddRow("fk_users_account", "account_id", "accounts", "id", "a", "a", "s", "", false, false, nil, false, true, false).
			AddRow("fk_users_owner", "owner_id", "owners", "id", "a", "a", "s", "", false, false, nil, true, false, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{
		{Name: "fk_users_account", Columns: []string{"account_id"}, ReferencedTable: "accounts", ReferencedColumns: []string{"id"}, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, NotValid: true},
		{Name: "fk_users_owner", Columns: []string{"owner_id"}, ReferencedTable: "owners", ReferencedColumns: []string{"id"}, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, NotEnforced: true},
	}, table.ForeignKeys)
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
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
	expectPostgreSQLMetadataBeforeForeignKeysWithChecksAndExclusions(mock, tableName, version, checks, sqlmock.NewRows([]string{"conname", "amname", "key_expression", "operator", "predicate", "condeferrable", "condeferred"}))
}

func expectPostgreSQLMetadataBeforeForeignKeysWithChecksAndExclusions(mock sqlmock.Sqlmock, tableName string, version string, checks *sqlmock.Rows, exclusions *sqlmock.Rows) {
	uniqueNulls := "index_metadata\\.indnullsnotdistinct"
	temporal := "constraint_data\\.conperiod"
	if version == "140000" {
		uniqueNulls = "FALSE"
		temporal = "FALSE"
	}
	mock.ExpectQuery("SELECT constraint_data\\.conname, attribute\\.attname, constraint_data\\.condeferrable, constraint_data\\.condeferred, " + uniqueNulls + ", .*, " + temporal + ", array_to_string\\(index_data\\.reloptions, ','\\), index_tablespace\\.spcname, index_metadata\\.indisreplident, CASE WHEN \\(index_collation\\.collation_oid <> attribute\\.attcollation OR attribute\\.attcollation <> type_data\\.typcollation\\) THEN collation_metadata\\.collname ELSE NULL END FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"conname", "attname", "condeferrable", "condeferred", "indnullsnotdistinct", "includes_columns", "conperiod", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	enforced := "constraint_data\\.conenforced"
	if version == "140000" {
		enforced = "TRUE"
	}
	mock.ExpectQuery("SELECT constraint_data\\.conname, pg_catalog\\.pg_get_expr\\(constraint_data\\.conbin, constraint_data\\.conrelid, true\\), constraint_data\\.connoinherit, constraint_data\\.convalidated, " + enforced + " FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(checks)
	mock.ExpectQuery("SELECT constraint_data\\.conname, access_method\\.amname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), operator_data\\.oprname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\), constraint_data\\.condeferrable, constraint_data\\.condeferred FROM pg_catalog\\.pg_constraint.*constraint_data\\.contype = 'x'").
		WithArgs(tableName).
		WillReturnRows(exclusions)
	mock.ExpectQuery("SELECT index_data\\.relname, index_metadata\\.indisunique, attribute\\.attname, pg_catalog\\.pg_get_indexdef\\(index_metadata\\.indexrelid, key_column\\.ordinal_position::int, true\\), access_method\\.amname, pg_catalog\\.pg_get_expr\\(index_metadata\\.indpred, index_metadata\\.indrelid, true\\).*NOT index_metadata\\.indisvalid.*array_to_string\\(index_data\\.reloptions, ','\\).*index_tablespace\\.spcname.*index_metadata\\.indisreplident, " + uniqueNulls + ", \\(key_option\\.value & 2\\) <> 0 FROM pg_catalog\\.pg_index").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows(postgreSQLIndexColumns))
}

// postgreSQLIndexColumns names the columns the PostgreSQL indexes query
// returns, in order: an index's identity and per-index facts (relname
// through predicate), then the per-key facts (descending, operator_class,
// collation), then the per-index include_columns list, and finally the
// index's own validity, storage parameters, tablespace, and replica
// identity facts.
var postgreSQLIndexColumns = []string{"relname", "indisunique", "attname", "key_expression", "amname", "predicate", "descending", "operator_class", "collation", "include_columns", "not_valid", "storage_parameters", "tablespace", "replica_identity", "nulls_not_distinct", "nulls_first"}

func expectPostgreSQLForeignKeys(mock sqlmock.Sqlmock, tableName string, version string) {
	expectPostgreSQLForeignKeysWithRows(mock, tableName, version, sqlmock.NewRows([]string{"conname", "local_column", "referenced_table", "referenced_column", "delete_action", "update_action", "match_type", "referenced_schema", "condeferrable", "condeferred", "delete_set_columns", "convalidated", "conenforced", "conperiod"}))
}

func expectPostgreSQLForeignKeysWithRows(mock sqlmock.Sqlmock, tableName string, version string, foreignKeys *sqlmock.Rows) {
	deleteSetColumns := "\\(SELECT string_agg\\(delete_set_attribute\\.attname, ',' ORDER BY delete_set_key\\.ordinal_position\\) FROM unnest\\(constraint_data\\.confdelsetcols\\) WITH ORDINALITY AS delete_set_key\\(attribute_number, ordinal_position\\) JOIN pg_catalog\\.pg_attribute AS delete_set_attribute ON delete_set_attribute\\.attrelid = constraint_data\\.conrelid AND delete_set_attribute\\.attnum = delete_set_key\\.attribute_number\\)"
	temporal := "constraint_data\\.conperiod"
	if version == "140000" {
		deleteSetColumns = "NULL"
		temporal = "FALSE"
	}
	enforced := "constraint_data\\.conenforced"
	if version == "140000" {
		enforced = "TRUE"
	}
	mock.ExpectQuery("SELECT constraint_data\\.conname, local_attribute\\.attname, referenced_table\\.relname, referenced_attribute\\.attname, constraint_data\\.confdeltype, constraint_data\\.confupdtype, constraint_data\\.confmatchtype, CASE WHEN referenced_namespace\\.nspname = current_schema\\(\\) THEN '' ELSE referenced_namespace\\.nspname END, constraint_data\\.condeferrable, constraint_data\\.condeferred, " + deleteSetColumns + ", constraint_data\\.convalidated, " + enforced + ", " + temporal + " FROM pg_catalog\\.pg_constraint").
		WithArgs(tableName).
		WillReturnRows(foreignKeys)
}

func TestMySQLInspectorRejectsPartialColumnMetadata(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("email", "varchar(255)", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "users", "CREATE TABLE `users` (`id` bigint NOT NULL, `email` varchar(255) NOT NULL, `secret` text NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")

	table, err := inspector.Table(t.Context(), "users")
	require.Equal(t, schema.TableDef{}, table)
	require.ErrorIs(t, err, inspect.ErrIncompleteMetadata)
	var incomplete *inspect.IncompleteMetadataError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, "users", incomplete.Table)
	require.Equal(t, 2, incomplete.Visible)
	require.Equal(t, 3, incomplete.Actual)
}

func TestMySQLInspectorRejectsZeroVisibleColumnMetadata(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}))
	expectMySQLCreateTable(mock, "users", "CREATE TABLE `users` (`id` bigint NOT NULL, `secret` text NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")

	table, err := inspector.Table(t.Context(), "users")
	require.Equal(t, schema.TableDef{}, table)
	require.ErrorIs(t, err, inspect.ErrIncompleteMetadata)
	var incomplete *inspect.IncompleteMetadataError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, "users", incomplete.Table)
	require.Equal(t, 0, incomplete.Visible)
	require.Equal(t, 2, incomplete.Actual)
}

// TestMySQLInspectorPropagatesOtherShowCreateTableErrors confirms that a
// SHOW CREATE TABLE failure the MySQL error 1146 detection does not
// positively recognize is not treated as "table not found": it propagates,
// carrying the original error and the wrapped message it has today.
//
// The last case is the regression the field-only match let through. An error
// declared outside the driver may carry a field named Number of the same kind
// holding a number of its own, and reading 1146 out of one made Table report
// a table that exists as missing while dropping the real failure. The
// detection now reads a number only from the driver's own error type, so
// every stand-in declared here propagates whatever number it carries. The
// genuine driver error still reaches TableNotFoundError, which
// TestMySQLInspectorReportsTableNotFoundAgainstLiveDatabase pins against a
// real server.
func TestMySQLInspectorPropagatesOtherShowCreateTableErrors(t *testing.T) {
	testCases := map[string]error{
		"a different MySQL error number":     &foreignNumberedError{Number: 1045, Message: "Access denied"},
		"a plain error with no Number field": errors.New("boom"),
		"MySQL error number 1146 carried by an error the driver did not declare": &foreignNumberedError{
			Number:  1146,
			Message: "connection reset mid-handshake",
		},
	}
	for name, showCreateTableErr := range testCases {
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
			columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
			mock.ExpectQuery(columnsQuery).
				WithArgs("ghosts").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}))
			mock.ExpectQuery("SHOW CREATE TABLE `ghosts`").
				WillReturnError(showCreateTableErr)

			table, err := inspector.Table(t.Context(), "ghosts")
			require.Equal(t, schema.TableDef{}, table)
			require.NotErrorIs(t, err, inspect.ErrTableNotFound)
			require.ErrorContains(t, err, `inspect: read MySQL table "ghosts" definition`)
			require.ErrorIs(t, err, showCreateTableErr)
		})
	}
}

// foreignNumberedError stands in for an error declared by a package other
// than github.com/go-sql-driver/mysql that reports a number of its own in a
// field named Number, the same name and kind *mysql.MySQLError uses. Whatever
// number it carries, that number is not a MySQL server error number, and
// inspect's MySQL error 1146 detection (mysqlErrorNumber in
// mysql_error_number.go) reads no number out of it: the detection matches the
// driver's error type by package path and name, which this local type does
// not have. That is why every test using this type expects the error to
// propagate.
type foreignNumberedError struct {
	Number  uint16
	Message string
}

func (e *foreignNumberedError) Error() string {
	return fmt.Sprintf("Error %d: %s", e.Number, e.Message)
}

func (e *foreignNumberedError) Is(target error) bool {
	other, ok := target.(*foreignNumberedError)
	return ok && other.Number == e.Number
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
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("active", "tinyint(1)", "NO", nil, nil, nil, "", "").
			AddRow("login_attempts", "tinyint", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "users", "CREATE TABLE `users` (`id` bigint NOT NULL, `active` tinyint(1) NOT NULL, `login_attempts` tinyint NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "active", Type: schema.BooleanType{}},
		{Name: "login_attempts", Type: schema.IntegerType{}},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)

	source, err := generate.PackageSource("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Active\s+bool$`, string(source))
	require.Regexp(t, `(?m)^\s*LoginAttempts\s+int64$`, string(source))
	require.Contains(t, string(source), "return r.Active, true")
	require.Contains(t, string(source), "return r.LoginAttempts, true")
}

func expectMySQLCreateTable(mock sqlmock.Sqlmock, tableName string, definition string) {
	mock.ExpectQuery("SHOW CREATE TABLE `" + tableName + "`").
		WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow(tableName, definition))
}

const mysqlStatisticsExpressionQuery = "SHOW COLUMNS FROM information_schema.statistics LIKE 'EXPRESSION'"
const mysqlStatisticsVisibilityQuery = "SHOW COLUMNS FROM information_schema.statistics LIKE 'IS_VISIBLE'"
const mysqlIndexesQuery = "SELECT index_name, non_unique = 0, column_name, sub_part, expression, collation, index_type, is_visible FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' ORDER BY index_name, seq_in_index"
const mysqlIndexesQueryWithoutExpressionOrVisibility = "SELECT index_name, non_unique = 0, column_name, sub_part, collation, index_type, TRUE FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' ORDER BY index_name, seq_in_index"

func expectMySQLStatisticsExpression(mock sqlmock.Sqlmock, present bool) {
	rows := sqlmock.NewRows([]string{"Field"})
	if present {
		rows.AddRow("EXPRESSION")
	}
	mock.ExpectQuery(mysqlStatisticsExpressionQuery).WillReturnRows(rows)
}

func expectMySQLStatisticsVisibility(mock sqlmock.Sqlmock, present bool) {
	rows := sqlmock.NewRows([]string{"Field"})
	if present {
		rows.AddRow("IS_VISIBLE")
	}
	mock.ExpectQuery(mysqlStatisticsVisibilityQuery).WillReturnRows(rows)
}

func expectMySQLIndexes(mock sqlmock.Sqlmock, tableName string, rows *sqlmock.Rows) {
	expectMySQLEmptyMetadata(mock, tableName)
	expectMySQLIndexQuery(mock, tableName, rows)
	expectMySQLEmptyForeignKeys(mock, tableName)
}

func expectMySQLIndexesBeforeError(mock sqlmock.Sqlmock, tableName string, rows *sqlmock.Rows) {
	expectMySQLEmptyMetadata(mock, tableName)
	expectMySQLIndexQuery(mock, tableName, rows)
}

func expectMySQLIndexQuery(mock sqlmock.Sqlmock, tableName string, rows *sqlmock.Rows) {
	expectMySQLStatisticsExpression(mock, true)
	expectMySQLStatisticsVisibility(mock, true)
	mock.ExpectQuery(mysqlIndexesQuery).
		WithArgs(tableName).
		WillReturnRows(rows)
}

func expectMySQLIndexesWithoutExpression(mock sqlmock.Sqlmock, tableName string, rows *sqlmock.Rows) {
	expectMySQLEmptyMetadata(mock, tableName)
	expectMySQLStatisticsExpression(mock, false)
	expectMySQLStatisticsVisibility(mock, false)
	mock.ExpectQuery(mysqlIndexesQueryWithoutExpressionOrVisibility).
		WithArgs(tableName).
		WillReturnRows(rows)
	expectMySQLEmptyForeignKeys(mock, tableName)
}

func expectMySQLColumnsAndPrimaryKey(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("email", "varchar(255)", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, tableName, "CREATE TABLE `"+tableName+"` (`id` bigint NOT NULL, `email` varchar(255) NOT NULL) ENGINE=InnoDB")
	mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
}

func expectMySQLEmptyMetadata(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT key_column_usage.constraint_name, key_column_usage.column_name, FALSE, FALSE, FALSE, NULL, FALSE, NULL, NULL, FALSE, NULL FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'UNIQUE' ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT check_constraints.constraint_name, check_constraints.check_clause, FALSE, TRUE, table_constraints.enforced = 'YES' FROM information_schema.check_constraints JOIN information_schema.table_constraints ON table_constraints.constraint_name = check_constraints.constraint_name AND table_constraints.table_schema = check_constraints.constraint_schema WHERE check_constraints.constraint_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'CHECK' ORDER BY check_constraints.constraint_name").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}))
}

func expectMySQLLegacyIndexes(mock sqlmock.Sqlmock, tableName string) {
	expectMySQLIndexesWithoutExpression(mock, tableName, sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "collation", "index_type", "is_visible"}))
}

func expectMySQLIndexesOnly(mock sqlmock.Sqlmock, tableName string) {
	expectMySQLStatisticsExpression(mock, false)
	expectMySQLStatisticsVisibility(mock, false)
	mock.ExpectQuery(mysqlIndexesQueryWithoutExpressionOrVisibility).
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "collation", "index_type", "is_visible"}))
}

func expectMySQLEmptyForeignKeys(mock sqlmock.Sqlmock, tableName string) {
	mock.ExpectQuery("SELECT key_column_usage.constraint_name, key_column_usage.column_name, key_column_usage.referenced_table_name, key_column_usage.referenced_column_name, CASE referential_constraints.delete_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.delete_rule END, CASE referential_constraints.update_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.update_rule END, CASE referential_constraints.match_option WHEN 'NONE' THEN 's' ELSE referential_constraints.match_option END, CASE WHEN key_column_usage.referenced_table_schema = DATABASE() THEN '' ELSE key_column_usage.referenced_table_schema END, FALSE, FALSE, NULL, TRUE, TRUE, FALSE FROM information_schema.key_column_usage JOIN information_schema.referential_constraints ON referential_constraints.constraint_schema = key_column_usage.constraint_schema AND referential_constraints.constraint_name = key_column_usage.constraint_name AND referential_constraints.table_name = key_column_usage.table_name WHERE key_column_usage.constraint_schema = DATABASE() AND key_column_usage.table_name = ? AND key_column_usage.referenced_table_name IS NOT NULL ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position").
		WithArgs(tableName).
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}))
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
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	uniqueQuery := "SELECT key_column_usage.constraint_name, key_column_usage.column_name, FALSE, FALSE, FALSE, NULL, FALSE, NULL, NULL, FALSE, NULL FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'UNIQUE' ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position"
	checksQuery := "SELECT check_constraints.constraint_name, check_constraints.check_clause, FALSE, TRUE, table_constraints.enforced = 'YES' FROM information_schema.check_constraints JOIN information_schema.table_constraints ON table_constraints.constraint_name = check_constraints.constraint_name AND table_constraints.table_schema = check_constraints.constraint_schema WHERE check_constraints.constraint_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'CHECK' ORDER BY check_constraints.constraint_name"
	foreignKeysQuery := "SELECT key_column_usage.constraint_name, key_column_usage.column_name, key_column_usage.referenced_table_name, key_column_usage.referenced_column_name, CASE referential_constraints.delete_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.delete_rule END, CASE referential_constraints.update_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.update_rule END, CASE referential_constraints.match_option WHEN 'NONE' THEN 's' ELSE referential_constraints.match_option END, CASE WHEN key_column_usage.referenced_table_schema = DATABASE() THEN '' ELSE key_column_usage.referenced_table_schema END, FALSE, FALSE, NULL, TRUE, TRUE, FALSE FROM information_schema.key_column_usage JOIN information_schema.referential_constraints ON referential_constraints.constraint_schema = key_column_usage.constraint_schema AND referential_constraints.constraint_name = key_column_usage.constraint_name AND referential_constraints.table_name = key_column_usage.table_name WHERE key_column_usage.constraint_schema = DATABASE() AND key_column_usage.table_name = ? AND key_column_usage.referenced_table_name IS NOT NULL ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).AddRow("id", "bigint", "NO", nil, nil, nil, "", "").AddRow("email", "varchar(255)", "NO", nil, nil, nil, "", "").AddRow("account_id", "bigint", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "users", "CREATE TABLE `users` (`id` bigint NOT NULL, `email` varchar(255) NOT NULL, `account_id` bigint NOT NULL) ENGINE=InnoDB")
	mock.ExpectQuery(primaryKeyQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	mock.ExpectQuery(uniqueQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "storage_parameters", "tablespace", "replica_identity", "collation"}).AddRow("uq_users_email", "email", false, false, false, nil, false, nil, nil, false, nil))
	mock.ExpectQuery(checksQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}).AddRow("chk_users_email", "email <> ''", false, true, true))
	expectMySQLIndexesOnly(mock, "users")
	mock.ExpectQuery(foreignKeysQuery).WithArgs("users").WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "referenced_table_name", "referenced_column_name", "delete_rule", "update_rule", "match_option", "referenced_schema", "deferrable", "initially_deferred", "delete_set_columns", "validated", "enforced", "temporal"}).AddRow("fk_users_account", "account_id", "accounts", "id", "c", "a", "s", "", false, false, nil, true, true, false))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{{Name: "uq_users_email", Columns: []string{"email"}}}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckDef{{Name: "chk_users_email", Expression: "email <> ''"}}, table.Checks)
	require.Equal(t, []schema.ForeignKeyDef{{Name: "fk_users_account", Columns: []string{"account_id"}, ReferencedTable: "accounts", ReferencedColumns: []string{"id"}, OnDelete: schema.Cascade, OnUpdate: schema.NoAction}}, table.ForeignKeys)

	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), "CONSTRAINT `uq_users_email` UNIQUE (`email`)")
	require.Contains(t, rendered.SQL(), "CONSTRAINT `chk_users_email` CHECK (email <> '')")
	require.Contains(t, rendered.SQL(), "CONSTRAINT `fk_users_account` FOREIGN KEY (`account_id`) REFERENCES `accounts` (`id`) ON DELETE CASCADE")
}

// TestMySQLInspectorRecordsCheckNotEnforced proves that a MySQL check
// constraint declared NOT ENFORCED is described rather than rejected:
// inspect used to fail the whole table on the first such check constraint,
// which would abort a sweep over a production schema the moment it reached
// one. MySQL's own check query already computes enforced from
// table_constraints.enforced, unlike no_inherit and validated, which it
// hardcodes FALSE and TRUE respectively because MySQL has no NO INHERIT or
// NOT VALID concept for check constraints.
func TestMySQLInspectorRecordsCheckNotEnforced(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	mock.ExpectQuery("SELECT key_column_usage.constraint_name, key_column_usage.column_name, FALSE, FALSE, FALSE, NULL, FALSE, NULL, NULL, FALSE, NULL FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'UNIQUE' ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "column_name", "deferrable", "initially_deferred", "nulls_not_distinct", "includes_columns", "temporal", "storage_parameters", "tablespace", "replica_identity", "collation"}))
	mock.ExpectQuery("SELECT check_constraints.constraint_name, check_constraints.check_clause, FALSE, TRUE, table_constraints.enforced = 'YES' FROM information_schema.check_constraints JOIN information_schema.table_constraints ON table_constraints.constraint_name = check_constraints.constraint_name AND table_constraints.table_schema = check_constraints.constraint_schema WHERE check_constraints.constraint_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'CHECK' ORDER BY check_constraints.constraint_name").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"constraint_name", "check_clause", "no_inherit", "validated", "enforced"}).AddRow("chk_users_email", "email <> ''", false, true, false))
	expectMySQLIndexesOnly(mock, "users")
	expectMySQLEmptyForeignKeys(mock, "users")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.CheckDef{{Name: "chk_users_email", Expression: "email <> ''", NotEnforced: true}}, table.Checks)
}

func TestMySQLInspectorScopesPrimaryKeyToTable(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("order_id", "bigint", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "users", "CREATE TABLE `users` (`id` bigint NOT NULL, `order_id` bigint NOT NULL) ENGINE=InnoDB")
	// MySQL names both tables' unnamed primary-key constraints PRIMARY.
	mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLLegacyIndexes(mock, "users")

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
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
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexesWithoutExpression(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "collation", "index_type", "is_visible"}).AddRow("users_email_uidx", true, "email", nil, "A", "BTREE", int64(1)))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{{Name: "users_email_uidx", Columns: []string{"email"}, Unique: true}}, table.Indexes)
}

// TestMySQLInspectorRecordsNonDefaultIndexMethod proves that a MySQL index
// built with a non-BTREE index type, such as FULLTEXT, is now described
// rather than rejected: inspect used to fail the whole table on the first
// such index, which would abort a sweep over a production schema the moment
// it reached one FULLTEXT index. The plain BTREE index alongside it keeps
// schema.IndexMethod's zero value, proving the field only records a method
// when it differs from the default.
func TestMySQLInspectorRecordsNonDefaultIndexMethod(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexesWithoutExpression(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "collation", "index_type", "is_visible"}).
		AddRow("users_email_uidx", true, "email", nil, "A", "BTREE", int64(1)).
		AddRow("users_email_fulltext_idx", false, "email", nil, nil, "FULLTEXT", int64(1)))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_uidx", Columns: []string{"email"}, Unique: true},
		{Name: "users_email_fulltext_idx", Columns: []string{"email"}, Method: "FULLTEXT"},
	}, table.Indexes)
}

// TestMySQLInspectorRecordsExpressionIndex proves that a MySQL functional
// index key is now described rather than rejected: inspect used to fail
// the whole table on the first functional index part, which would abort a
// sweep over a production schema the moment it reached one. The plain
// index alongside it keeps IndexDef.Expressions nil and Columns populated,
// proving Expressions is only used for an index that actually has an
// expression key.
func TestMySQLInspectorRecordsExpressionIndex(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexes(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}).
		AddRow("users_email_uidx", true, "email", nil, nil, "A", "BTREE", int64(1)).
		AddRow("users_lower_email_idx", false, nil, nil, "lower(`email`)", nil, "BTREE", int64(1)))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_uidx", Columns: []string{"email"}, Unique: true},
		{Name: "users_lower_email_idx", Expressions: []string{"lower(`email`)"}},
	}, table.Indexes)
}

// TestMySQLInspectorRecordsInvisibleNonDefaultMethodUniqueIndex proves that
// an invisible unique FULLTEXT index is now described rather than rejected:
// inspect used to fail the whole table on an invisible unique index, which
// would abort a sweep over a production schema the moment it reached one.
// The non-BTREE method and the invisibility are recorded together, proving
// neither short-circuits inspection of the other.
func TestMySQLInspectorRecordsInvisibleNonDefaultMethodUniqueIndex(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexes(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}).
		AddRow("users_email_uidx", true, "email", nil, nil, "A", "FULLTEXT", "NO"))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_uidx", Columns: []string{"email"}, Unique: true, Method: "FULLTEXT", Invisible: true},
	}, table.Indexes)
}

// TestMySQLInspectorRecordsPrefixIndexPart proves that a MySQL index part
// over a column prefix is now described rather than rejected: inspect used
// to fail the whole table on the first prefix index part, which would abort
// a sweep over a production schema the moment it reached one.
func TestMySQLInspectorRecordsPrefixIndexPart(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexes(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}).
		AddRow("users_email_prefix_uidx", true, "email", int64(4), nil, "A", "BTREE", true))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_prefix_uidx", Unique: true, Keys: []schema.IndexKeyDef{{Expression: "email", PrefixLength: 4}}},
	}, table.Indexes)
}

func TestMySQLInspectorRejectsUnsupportedIndexParts(t *testing.T) {
	tests := []struct {
		name       string
		indexName  string
		column     any
		prefix     any
		expression any
		reason     string
	}{
		{name: "non-column", indexName: "users_email_non_column_idx", reason: "non-column index parts"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, database.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})

			inspector, err := inspect.New(database, dialect.MySQL())
			require.NoError(t, err)
			expectMySQLColumnsAndPrimaryKey(mock, "users")
			expectMySQLIndexesBeforeError(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}).
				AddRow(test.indexName, true, test.column, test.prefix, test.expression, nil, "BTREE", true))

			_, err = inspector.Table(t.Context(), "users")
			require.EqualError(t, err, fmt.Sprintf("inspect: index %q cannot be represented: rasql does not support MySQL %s", test.indexName, test.reason))
		})
	}
}

// TestMySQLInspectorRecordsDescendingUniqueIndexPart proves that a MySQL
// unique index part ordered DESC is now described rather than rejected:
// inspect used to fail the whole table on the first descending unique index
// part, which would abort a sweep over a production schema the moment it
// reached one.
func TestMySQLInspectorRecordsDescendingUniqueIndexPart(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexes(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}).
		AddRow("users_email_uidx", true, "email", nil, nil, "D", "BTREE", true))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_email_uidx", Unique: true, Keys: []schema.IndexKeyDef{{Expression: "email", Descending: true}}},
	}, table.Indexes)
}

func TestMySQLInspectorRejectsUnknownIndexVisibility(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	expectMySQLColumnsAndPrimaryKey(mock, "users")
	expectMySQLIndexesBeforeError(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}).
		AddRow("users_email_uidx", true, "email", nil, nil, "A", "BTREE", "MAYBE"))

	_, err = inspector.Table(t.Context(), "users")
	require.EqualError(t, err, `inspect: scan index: sql: Scan error on column index 7, name "is_visible": inspect: MySQL index visibility "MAYBE" must be YES or NO`)
}

func TestMySQLInspectorReadsOrdinaryIndexesLegacy(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("email", "varchar(255)", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "users", "CREATE TABLE `users` (`id` bigint NOT NULL, `email` varchar(255) NOT NULL) ENGINE=InnoDB")
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("users").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexesWithoutExpression(mock, "users", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "collation", "index_type", "is_visible"}).AddRow("users_email_idx", false, "email", nil, "A", "BTREE", true))

	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{{Name: "users_email_idx", Columns: []string{"email"}}}, table.Indexes)
}

// TestMySQLInspectorRecordsUnsignedIntegerColumn follows one unsigned column
// the whole way: the catalog reports bigint(20) unsigned, the descriptor
// records it, the MySQL renderer puts the UNSIGNED back, and the generator
// emits a uint64 field for it. Before signedness reached schema.ColumnDef this
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
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint unsigned", "NO", nil, int64(20), int64(0), "", "").
			AddRow("sequence", "bigint", "NO", nil, int64(19), int64(0), "", ""))
	expectMySQLCreateTable(mock, "events", "CREATE TABLE `events` (`id` bigint unsigned NOT NULL, `sequence` bigint NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "events", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{Unsigned: true}},
		{Name: "sequence", Type: schema.IntegerType{}},
	}, table.Columns)

	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), "`id` BIGINT UNSIGNED NOT NULL")
	require.Contains(t, rendered.SQL(), "`sequence` BIGINT NOT NULL")

	source, err := generate.PackageSource("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+uint64$`, string(source))
	require.Regexp(t, `(?m)^\s*Sequence\s+int64$`, string(source))
}

// TestMySQLInspectorRecordsIntegerDisplayWidthAndZeroFill covers the two
// integer modifiers inspection used to reject outright: a stated display
// width, such as the 11 in int(11), and ZEROFILL, which implies UNSIGNED.
// Both now round-trip through inspection instead of making the whole table
// unrepresentable, but render.CreateTable still refuses to build DDL for
// either: see TestCreateTableRejectsIntegerDisplayWidth and
// TestCreateTableRejectsIntegerZeroFill in the render package for that
// refusal.
func TestMySQLInspectorRecordsIntegerDisplayWidthAndZeroFill(t *testing.T) {
	tests := map[string]struct {
		columnType string
		want       schema.ColumnType
	}{
		"display width alone": {
			columnType: "int(11)",
			want:       schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(11)},
		},
		"display width with unsigned": {
			columnType: "bigint(20) unsigned",
			want:       schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(20)},
		},
		"unsigned zerofill states its display width": {
			columnType: "int(10) unsigned zerofill",
			want:       schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(10), ZeroFill: true},
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("id", test.columnType, "NO", nil, int64(20), int64(0), "", ""))
			expectMySQLCreateTable(mock, "events", "CREATE TABLE `events` (`id` "+test.columnType+" NOT NULL) ENGINE=InnoDB")
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
			expectMySQLIndexes(mock, "events", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

			table, err := inspector.Table(t.Context(), "events")
			require.NoError(t, err)
			require.Equal(t, []schema.ColumnDef{{Name: "id", Type: test.want}}, table.Columns)

			_, err = render.CreateTable(dialect.MySQL(), table)
			require.Error(t, err, "render still refuses DDL for a display width or ZEROFILL until it can build one")
			require.ErrorContains(t, err, `"id"`)
			require.ErrorContains(t, err, "can describe but not yet render")
		})
	}
}

// TestMySQLInspectorRecordsGeneratedColumns covers both MySQL generated
// column storage kinds as information_schema.columns.EXTRA spells them:
// "STORED GENERATED", which MySQL writes into the table, and "VIRTUAL
// GENERATED", which MySQL computes each time the column is read. Before
// this feature existed, MySQL inspection selected no generated-column
// metadata at all, so a generated column round-tripped indistinguishably
// from an ordinary, writable one; render.CreateTable still refuses to build
// DDL for one, see TestCreateTableRejectsGeneratedColumn in the render
// package for that refusal.
func TestMySQLInspectorRecordsGeneratedColumns(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs("measurements").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("celsius", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("fahrenheit_stored", "bigint", "NO", nil, nil, nil, "STORED GENERATED", "celsius * 9 / 5 + 32").
			AddRow("fahrenheit_virtual", "bigint", "NO", nil, nil, nil, "VIRTUAL GENERATED", "celsius * 9 / 5 + 32"))
	expectMySQLCreateTable(mock, "measurements", "CREATE TABLE `measurements` (`id` bigint NOT NULL, `celsius` bigint NOT NULL, `fahrenheit_stored` bigint GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED NOT NULL, `fahrenheit_virtual` bigint GENERATED ALWAYS AS (celsius * 9 / 5 + 32) VIRTUAL NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
	mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
		WithArgs("measurements").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "measurements", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

	table, err := inspector.Table(t.Context(), "measurements")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "celsius", Type: schema.IntegerType{}},
		{
			Name:                "fahrenheit_stored",
			Type:                schema.IntegerType{},
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedStored,
		},
		{
			Name:                "fahrenheit_virtual",
			Type:                schema.IntegerType{},
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedVirtual,
		},
	}, table.Columns)
	require.NoError(t, table.Validate())

	_, err = render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `"fahrenheit_stored"`)
	require.ErrorContains(t, err, "can describe but not yet render")
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("id", test.columnType, "NO", nil, int64(20), int64(0), "", ""))

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
		want       schema.ColumnDef
	}{
		"bigint":                            {columnType: "bigint", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{}}},
		"bigint with width":                 {columnType: "bigint(20)", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(20)}}},
		"bigint unsigned":                   {columnType: "bigint unsigned", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		"bigint width unsigned":             {columnType: "bigint(20) unsigned", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(20)}}},
		"int unsigned":                      {columnType: "int(10) unsigned", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(10)}}},
		"integer alias":                     {columnType: "integer", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{}}},
		"smallint unsigned":                 {columnType: "smallint unsigned", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true}}},
		"mediumint":                         {columnType: "mediumint", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{}}},
		"tinyint":                           {columnType: "tinyint", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{}}},
		"tinyint(1) is a boolean":           {columnType: "tinyint(1)", want: schema.ColumnDef{Name: "id", Type: schema.BooleanType{}}},
		"unsigned tinyint(1) is an integer": {columnType: "tinyint(1) unsigned", want: schema.ColumnDef{Name: "id", Type: schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(1)}}},
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("id", test.columnType, "NO", nil, int64(20), int64(0), "", ""))
			expectMySQLCreateTable(mock, "events", "CREATE TABLE `events` (`id` "+test.columnType+" NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
			expectMySQLIndexes(mock, "events", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

			table, err := inspector.Table(t.Context(), "events")
			require.NoError(t, err)
			require.Equal(t, []schema.ColumnDef{test.want}, table.Columns)
		})
	}
}

// TestMySQLInspectorNormalizesTextWidth covers schema.TextType.Width
// preservation: CHAR(n) and VARCHAR(n) round-trip their stated width, while
// TEXT, ENUM and SET normalize to an unstated width exactly as they did
// before TextType had one. MySQL never reports TEXT as TEXT(n), and ENUM/SET
// carry a value list this package does not otherwise preserve, so none of
// the three has a plain numeric length to record. CHAR additionally
// normalizes with Fixed set, since COLUMN_TYPE distinguishes CHAR from
// VARCHAR, and re-renders as CHAR(n) rather than VARCHAR(n).
func TestMySQLInspectorNormalizesTextWidth(t *testing.T) {
	tests := map[string]struct {
		columnType string
		want       schema.ColumnType
	}{
		"varchar with width": {columnType: "varchar(255)", want: schema.TextType{Width: schema.NewTextWidth(255)}},
		"char with width":    {columnType: "char(36)", want: schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}},
		"varchar zero width": {columnType: "varchar(0)", want: schema.TextType{Width: schema.NewTextWidth(0)}},
		"bare text":          {columnType: "text", want: schema.TextType{}},
		"enum has no width":  {columnType: "enum('a','b')", want: schema.TextType{}},
		"set has no width":   {columnType: "set('a','b')", want: schema.TextType{}},
		"mediumtext":         {columnType: "mediumtext", want: schema.TextType{}},
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("value", test.columnType, "NO", nil, nil, nil, "", ""))
			expectMySQLCreateTable(mock, "events", "CREATE TABLE `events` (`value` "+test.columnType+" NOT NULL) ENGINE=InnoDB")
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
			expectMySQLIndexes(mock, "events", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

			table, err := inspector.Table(t.Context(), "events")
			require.NoError(t, err)
			require.Equal(t, []schema.ColumnDef{{Name: "value", Type: test.want}}, table.Columns)
		})
	}
}

// TestMySQLInspectorRoundTripsUUIDWithoutSpuriousDiff is a regression test
// for the defect this package's Fixed support fixes: schema.UUIDType renders
// CHAR(36) on MySQL, but a live CHAR(36) column used to inspect with an
// unstated fixed-ness, re-rendering as VARCHAR(36) and making
// migrate/diff/mysql report "column events.id changed" with a
// manualMigrationError and an empty Plan instead of the empty, error-free
// plan an unchanged column deserves. This exercises the same
// render.CreateTable -> inspect -> LiveSources -> Diff path diff-live uses.
// The inspected descriptor still says TextType where the desired schema
// says UUIDType: rasql cannot recover the logical type MySQL's catalog
// never recorded, only stop it from producing a phantom diff.
func TestMySQLInspectorRoundTripsUUIDWithoutSpuriousDiff(t *testing.T) {
	desired := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.UUIDType{}},
		},
		PrimaryKey: []string{"id"},
	}
	createTable, err := render.CreateTable(dialect.MySQL(), desired)
	require.NoError(t, err)

	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "char(36)", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "events", "CREATE TABLE `events` (`id` char(36) NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
	mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "events", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

	live, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}, live.Columns[0].Type)

	analyzer := mysqldiff.New()
	baseline, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: createTable.SQL()}})
	require.NoError(t, err)
	liveSources, err := analyzer.LiveSources(live)
	require.NoError(t, err)
	liveSnapshot, err := analyzer.Parse(liveSources)
	require.NoError(t, err)

	plan, err := analyzer.Diff(baseline, liveSnapshot)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

// TestMySQLInspectorRoundTripsFixedWidthTextWithoutSpuriousDiff covers a
// hand-declared fixed-width text column, not just schema.UUIDType: any
// MySQL CHAR(n) column hit the same "column changed" diagnostic before
// Fixed existed, since inspecting CHAR(n) produced an unstated fixed-ness
// that re-rendered as VARCHAR(n).
func TestMySQLInspectorRoundTripsFixedWidthTextWithoutSpuriousDiff(t *testing.T) {
	desired := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "code", Type: schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}},
		},
		PrimaryKey: []string{"id"},
	}
	createTable, err := render.CreateTable(dialect.MySQL(), desired)
	require.NoError(t, err)

	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, "", "").
			AddRow("code", "char(10)", "NO", nil, nil, nil, "", ""))
	expectMySQLCreateTable(mock, "events", "CREATE TABLE `events` (`id` bigint NOT NULL, `code` char(10) NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")
	mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("id"))
	expectMySQLIndexes(mock, "events", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

	live, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}, live.Columns[1].Type)

	analyzer := mysqldiff.New()
	baseline, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: createTable.SQL()}})
	require.NoError(t, err)
	liveSources, err := analyzer.LiveSources(live)
	require.NoError(t, err)
	liveSnapshot, err := analyzer.Parse(liveSources)
	require.NoError(t, err)

	plan, err := analyzer.Diff(baseline, liveSnapshot)
	require.NoError(t, err)
	require.Empty(t, plan.Statements)
}

// TestMySQLInspectorMatchesTextColumnTypeExactly covers the two ways a
// CHAR or VARCHAR declaration can fail to state a plain numeric width: a
// missing width entirely and a trailing modifier, which this package cannot
// record and would otherwise be dropped silently.
func TestMySQLInspectorMatchesTextColumnTypeExactly(t *testing.T) {
	tests := map[string]struct {
		columnType string
		wantErr    string
	}{
		"varchar without width": {
			columnType: "varchar",
			wantErr:    "a VARCHAR column must be declared VARCHAR(width)",
		},
		"malformed width": {
			columnType: "varchar(twenty)",
			wantErr:    "a VARCHAR column must be declared VARCHAR(width)",
		},
		"zerofill modifier": {
			// This shape is synthetic: MySQL's own catalog never trails a
			// CHAR/VARCHAR declaration with anything after its width.
			// CHARACTER SET and COLLATE are separate information_schema.columns
			// columns, never appended to COLUMN_TYPE, and the legacy BINARY
			// attribute is canonicalized to an explicit COLLATE clause at
			// CREATE TABLE time (MySQL Worklog #13068), so it never reaches
			// COLUMN_TYPE as literal "BINARY" text either. The row is
			// fabricated to exercise this defensive branch, which genuine
			// MySQL server data cannot reach.
			columnType: "varchar(255) zerofill",
			wantErr:    "must carry no ZEROFILL modifier",
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("events").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("id", test.columnType, "NO", nil, nil, nil, "", ""))

			_, err = inspector.Table(t.Context(), "events")
			require.ErrorContains(t, err, test.wantErr)
			require.ErrorContains(t, err, `"id"`)
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
	columnsQuery := "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position"
	primaryKeyQuery := "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position"
	mock.ExpectQuery(columnsQuery).
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("amount", "decimal(10,2)", "NO", nil, int64(10), int64(2), "", ""))
	expectMySQLCreateTable(mock, "payments", "CREATE TABLE `payments` (`amount` decimal(10,2) NOT NULL) ENGINE=InnoDB")
	mock.ExpectQuery(primaryKeyQuery).
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	expectMySQLIndexes(mock, "payments", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

	table, err := inspector.Table(t.Context(), "payments")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}},
	}, table.Columns)
}

// TestMySQLInspectorMatchesDecimalColumnTypeExactly covers the ways a MySQL
// COLUMN_TYPE can look like a decimal without being one this package can
// represent. The catalog is read from a server the application may not
// control, so a decimal is recognized from the whole declaration: catalog
// text that merely contains DECIMAL or NUMERIC is an unsupported type, and a
// declaration carrying a modifier other than UNSIGNED or UNSIGNED ZEROFILL
// (see TestMySQLInspectorRecordsDecimalUnsignedAndZeroFill for those two) is
// refused rather than silently re-rendered without it.
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
		"zerofill without unsigned": {
			// MySQL's own catalog always spells ZEROFILL together with
			// UNSIGNED (see mysqlDecimalDeclaration's doc comment), so this
			// shape never comes from a real server; it still must not be
			// silently accepted.
			columnType: "decimal(10,2) zerofill",
			wantErr:    "must carry no ZEROFILL modifier",
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("amount", test.columnType, "NO", nil, int64(10), int64(2), "", ""))

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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("amount", columnType, "NO", nil, int64(10), int64(2), "", ""))
			expectMySQLCreateTable(mock, "payments", "CREATE TABLE `payments` (`amount` "+columnType+" NOT NULL) ENGINE=InnoDB")
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
			expectMySQLIndexes(mock, "payments", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

			table, err := inspector.Table(t.Context(), "payments")
			require.NoError(t, err)
			require.Equal(t, []schema.ColumnDef{
				{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}},
			}, table.Columns)
		})
	}
}

// TestMySQLInspectorRecordsDecimalUnsignedAndZeroFill proves that a MySQL
// DECIMAL or NUMERIC column carrying UNSIGNED, or UNSIGNED together with
// ZEROFILL, is now described rather than rejected: inspect used to fail the
// whole table on the first such column, which would abort a sweep over a
// production schema the moment it reached one.
func TestMySQLInspectorRecordsDecimalUnsignedAndZeroFill(t *testing.T) {
	tests := map[string]struct {
		columnType string
		want       schema.ColumnType
	}{
		"unsigned": {
			columnType: "decimal(10,2) unsigned",
			want:       schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true},
		},
		"unsigned zerofill": {
			columnType: "decimal(10,2) unsigned zerofill",
			want:       schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true},
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
			mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
					AddRow("amount", test.columnType, "NO", nil, int64(10), int64(2), "", ""))
			expectMySQLCreateTable(mock, "payments", "CREATE TABLE `payments` (`amount` "+test.columnType+" NOT NULL) ENGINE=InnoDB")
			mock.ExpectQuery("SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position").
				WithArgs("payments").
				WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
			expectMySQLIndexes(mock, "payments", sqlmock.NewRows([]string{"index_name", "unique", "column_name", "sub_part", "expression", "collation", "index_type", "is_visible"}))

			table, err := inspector.Table(t.Context(), "payments")
			require.NoError(t, err)
			require.Equal(t, []schema.ColumnDef{
				{Name: "amount", Type: test.want},
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
	mock.ExpectQuery("SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale, extra, generation_expression FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position").
		WithArgs("payments").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "column_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "extra", "generation_expression"}).
			AddRow("amount", "decimal(10,2)", "NO", nil, int64(10), nil, "", ""))

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
	mock.ExpectQuery("PRAGMA \"main\".table_xinfo(\"payments\")").
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
	mock.ExpectQuery("PRAGMA \"main\".table_xinfo(\"events\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "sequence", "INTEGER", 1, nil, 2, 0).
			AddRow(1, "stream_id", "TEXT", 1, nil, 1, 0).
			AddRow(2, "payload", "BLOB", 0, nil, 0, 0))
	mock.ExpectQuery("SELECT sql FROM \"main\".sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).
			AddRow("CREATE TABLE events (sequence INTEGER, stream_id TEXT, payload BLOB)"))
	mock.ExpectQuery(`PRAGMA "main".index_list("events")`).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}))
	mock.ExpectQuery(`PRAGMA "main".foreign_key_list("events")`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}))

	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []string{"stream_id", "sequence"}, table.PrimaryKey)
	require.Equal(t, schema.BytesType{}, table.Columns[2].Type)
	require.True(t, table.Columns[2].Nullable)
}

func TestSQLiteInspectorRejectsIncompleteColumnMetadata(t *testing.T) {
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
			AddRow("main", "events", "table", 2, 0, 0))
	mock.ExpectQuery("PRAGMA \"main\".table_xinfo(\"events\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "id", "INTEGER", 1, nil, 1, 0))

	table, err := inspector.Table(t.Context(), "events")
	require.Equal(t, schema.TableDef{}, table)
	require.ErrorIs(t, err, inspect.ErrIncompleteMetadata)
	var incomplete *inspect.IncompleteMetadataError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, "events", incomplete.Table)
	require.Equal(t, 1, incomplete.Visible)
	require.Equal(t, 2, incomplete.Actual)
}

func TestSQLiteInspectorRejectsCreateTableAsSelectDefinition(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list(\"copy\")").
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}).
			AddRow("main", "copy", "table", 1, 0, 0))
	mock.ExpectQuery("PRAGMA \"main\".table_xinfo(\"copy\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "id", "INTEGER", 0, nil, 0, 0))
	mock.ExpectQuery("SELECT sql FROM \"main\".sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("copy").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).
			AddRow("CREATE TABLE copy AS SELECT id FROM source"))

	table, err := inspector.Table(t.Context(), "copy")
	require.Equal(t, schema.TableDef{}, table)
	require.ErrorContains(t, err, "CREATE TABLE AS SELECT")
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
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)

	source, err := generate.PackageSource("generated", table)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+int64$`, string(source))
	require.NotContains(t, string(source), "ID *int64")
}

func TestSQLiteInspectorMarksIntegerPrimaryKeyAsNonNullableWithAttachedComments(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	tests := []struct {
		name        string
		tableName   string
		declaration string
	}{
		{
			name:        "line comment after INTEGER",
			tableName:   "events_integer_line",
			declaration: "CREATE TABLE events_integer_line (id INTEGER-- comment\nPRIMARY KEY, payload BLOB)",
		},
		{
			name:        "block comment after INTEGER",
			tableName:   "events_integer_block",
			declaration: "CREATE TABLE events_integer_block (id INTEGER/* comment */PRIMARY KEY, payload BLOB)",
		},
		{
			name:        "line comment after PRIMARY KEY",
			tableName:   "events_primary_key_line",
			declaration: "CREATE TABLE events_primary_key_line (id INTEGER PRIMARY KEY-- comment\n, payload BLOB)",
		},
		{
			name:        "block comment after PRIMARY KEY",
			tableName:   "events_primary_key_block",
			declaration: "CREATE TABLE events_primary_key_block (id INTEGER PRIMARY KEY/* comment */, payload BLOB)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.ExecContext(t.Context(), test.declaration)
			require.NoError(t, err)

			inspector, err := inspect.New(database, dialect.SQLite())
			require.NoError(t, err)
			table, err := inspector.Table(t.Context(), test.tableName)
			require.NoError(t, err)
			require.Equal(t, []string{"id"}, table.PrimaryKey)
			require.False(t, table.Columns[0].Nullable)
		})
	}
}

func TestSQLiteInspectorReadsTempTable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TEMP TABLE selected_temp (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "selected_temp")
	require.NoError(t, err)
	require.Equal(t, "selected_temp", table.Name)
	require.Equal(t, []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
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
	mock.ExpectQuery("PRAGMA \"main\".table_xinfo(\"Members\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "id", "INTEGER", 0, nil, 1, 0))
	mock.ExpectQuery("SELECT sql FROM \"main\".sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("Members").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).
			AddRow("CREATE TABLE Members (id INTEGER PRIMARY KEY)"))
	mock.ExpectQuery(`PRAGMA "main".index_list("Members")`).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}))
	mock.ExpectQuery(`PRAGMA "main".foreign_key_list("Members")`).
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
	require.Equal(t, []schema.UniqueDef{{Columns: []string{"email"}}}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckDef{{Expression: "length(email) > 0"}}, table.Checks)
	require.Equal(t, []schema.ForeignKeyDef{{
		Columns:           []string{"parent_id"},
		ReferencedTable:   "parents",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.NoAction,
		OnUpdate:          schema.NoAction,
	}}, table.ForeignKeys)
}

// TestSQLiteInspectorReportsNilForAbsentConstraintLists proves that a
// SQLite table with no unique constraints, checks, indexes, or foreign
// keys reports each of those TableDef slice fields as nil, matching the
// PostgreSQL and MySQL inspection paths (readUniqueConstraints, readChecks,
// readIndexes, and readForeignKeys all declare their accumulator with `var
// ... []T`, never `make([]T, 0)`). The SQLite-specific builders used to
// diverge, each starting from `make([]T, 0)`, so a table with none of these
// facts came back with a non-nil empty slice instead — a difference
// TableDef.Clone's own make(..., len(...)) bug happened to paper over by
// manufacturing the same non-nil empty slice on every clone, regardless of
// what inspection actually reported.
func TestSQLiteInspectorReportsNilForAbsentConstraintLists(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE plain (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "plain")
	require.NoError(t, err)
	require.Nil(t, table.UniqueConstraints)
	require.Nil(t, table.Checks)
	require.Nil(t, table.Indexes)
	require.Nil(t, table.ForeignKeys)
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
	require.Equal(t, []schema.UniqueDef{{Columns: []string{"email"}}}, table.UniqueConstraints)
	require.Equal(t, []schema.CheckDef{{Expression: "parent_id > 0"}}, table.Checks)
	require.Equal(t, []schema.ForeignKeyDef{{
		Columns:           []string{"parent_id"},
		ReferencedTable:   "parents",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.Cascade,
		OnUpdate:          schema.SetNull,
	}}, table.ForeignKeys)
}

// TestSQLiteInspectorRecordsForeignKeyFacts proves that a deferrable
// foreign key and one with a MATCH clause are now described rather than
// rejected: inspect used to fail the whole table on a DEFERRABLE or
// INITIALLY clause anywhere in the CREATE TABLE definition, which would
// abort a sweep over a production schema the moment it reached one such
// foreign key. It also proves the PRAGMA foreign_key_list id order (last
// declared first) is correctly reversed back to declaration order when
// zipping in the MATCH and deferrability clauses read from the table's own
// CREATE TABLE text, and that a bare DEFERRABLE with no INITIALLY clause
// defaults to INITIALLY IMMEDIATE.
func TestSQLiteInspectorRecordsForeignKeyFacts(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE parents (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE owners (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE children (
		id INTEGER PRIMARY KEY,
		parent_id INTEGER REFERENCES parents(id) MATCH FULL DEFERRABLE INITIALLY DEFERRED,
		owner_id INTEGER,
		FOREIGN KEY (owner_id) REFERENCES owners(id) MATCH PARTIAL DEFERRABLE
	)`)
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "children")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{
		{Columns: []string{"owner_id"}, ReferencedTable: "owners", ReferencedColumns: []string{"id"}, Match: schema.MatchPartial, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, Deferrable: schema.DeferrableInitiallyImmediate},
		{Columns: []string{"parent_id"}, ReferencedTable: "parents", ReferencedColumns: []string{"id"}, Match: schema.MatchFull, OnDelete: schema.NoAction, OnUpdate: schema.NoAction, Deferrable: schema.DeferrableInitiallyDeferred},
	}, table.ForeignKeys)
}

// TestSQLiteInspectorRecordsNotDeferrableForeignKey proves that an explicit
// NOT DEFERRABLE clause, and an omitted one, both name
// schema.Deferrability's zero value, since they mean the same thing.
func TestSQLiteInspectorRecordsNotDeferrableForeignKey(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE parents (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) NOT DEFERRABLE INITIALLY IMMEDIATE)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "children")
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{{
		Columns:           []string{"parent_id"},
		ReferencedTable:   "parents",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.NoAction,
		OnUpdate:          schema.NoAction,
	}}, table.ForeignKeys)
}

// TestSQLiteInspectorRejectsUnsupportedForeignKeyMatchType proves that an
// unrecognized MATCH name is still rejected, distinct from MATCH FULL or
// MATCH PARTIAL, which TestSQLiteInspectorRecordsForeignKeyFacts proves
// inspect now describes.
func TestSQLiteInspectorRejectsUnsupportedForeignKeyMatchType(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE parents (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (parent_id INTEGER REFERENCES parents(id) MATCH BOGUS)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = inspector.Table(t.Context(), "children")
	require.ErrorContains(t, err, "MATCH BOGUS is unsupported")
}

// TestSQLiteInspectorRecordsUniqueConstraintConflictResolution proves that a
// UNIQUE constraint's ON CONFLICT clause, on both a column-level and a
// table-level constraint, is now described rather than rejected: inspect
// used to fail the whole table on any ON CONFLICT clause, which would abort
// a sweep over a production schema the moment it reached one such
// constraint.
func TestSQLiteInspectorRecordsUniqueConstraintConflictResolution(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (
		id INTEGER PRIMARY KEY,
		email TEXT UNIQUE ON CONFLICT REPLACE,
		name TEXT,
		nickname TEXT,
		UNIQUE (name, nickname) ON CONFLICT IGNORE
	)`)
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "members")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Columns: []string{"email"}, OnConflict: schema.ConflictReplace},
		{Columns: []string{"name", "nickname"}, OnConflict: schema.ConflictIgnore},
	}, table.UniqueConstraints)
}

// TestSQLiteInspectorRecordsDefaultConflictResolution proves that an
// explicit ON CONFLICT ABORT clause, and an omitted one, both name
// schema.ConflictResolution's zero value, since SQLite's own default
// conflict-resolution algorithm is ABORT, so the two mean the same thing —
// the same fold TestSQLiteInspectorRecordsNotDeferrableForeignKey proves
// for an explicit NOT DEFERRABLE foreign-key clause.
func TestSQLiteInspectorRecordsDefaultConflictResolution(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT UNIQUE ON CONFLICT ABORT, name TEXT UNIQUE)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "members")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Columns: []string{"email"}},
		{Columns: []string{"name"}},
	}, table.UniqueConstraints)
}

// TestSQLiteInspectorRecordsUniqueConstraintKeyDetails proves that a
// UNIQUE constraint's own DESC ordering and non-default collation are now
// described on UniqueDef.Keys rather than rejected: inspect used to fail
// the whole table on the first such constraint, which would abort a sweep
// over a production schema the moment it reached one. UniqueDef.Keys
// reuses schema.IndexKeyDef, the same type IndexDef.Keys uses for a
// regular index's own per-key facts, which
// TestSQLiteInspectorRecordsIndexKeyDetails proves the same way for an
// index. The plain constraint alongside them keeps UniqueDef.Keys's zero
// value, proving the field only records key details when a constraint
// actually has one. The collation name is spelled lowercase in both the
// source SQL and the expected value: unlike IndexDef.Keys, which reads a
// regular index's own collation back from PRAGMA index_xinfo verbatim,
// UniqueDef.Keys is read from the constraint's own parsed CREATE TABLE
// text, which sqlitequery's parser folds to lowercase for an unquoted
// identifier, the same folding every other AST-derived identifier in this
// package (a column or constraint name, for instance) already carries.
func TestSQLiteInspectorRecordsUniqueConstraintKeyDetails(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE members (id INTEGER PRIMARY KEY, email TEXT, code TEXT, UNIQUE (email COLLATE nocase DESC), UNIQUE (code))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "members")
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{
		{Keys: []schema.IndexKeyDef{{Expression: "email", Descending: true, Collation: "nocase"}}},
		{Columns: []string{"code"}},
	}, table.UniqueConstraints)
}

// TestSQLiteInspectorRecordsIndexKeyDetails proves that a descending key and
// a non-default per-key collation are now described rather than rejected:
// inspect used to fail the whole table on the first index carrying either,
// which would abort a sweep over a production schema the moment it reached
// one. The plain index alongside them keeps IndexDef.Keys's zero value,
// proving the field only records key details when an index actually has
// them.
func TestSQLiteInspectorRecordsIndexKeyDetails(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "database.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER, name TEXT)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX children_parent_idx ON children (parent_id)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX children_parent_desc_idx ON children (parent_id DESC)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX children_name_nocase_idx ON children (name COLLATE NOCASE)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "children")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "children_name_nocase_idx", Keys: []schema.IndexKeyDef{{Expression: "name", Collation: "NOCASE"}}},
		{Name: "children_parent_desc_idx", Keys: []schema.IndexKeyDef{{Expression: "parent_id", Descending: true}}},
		{Name: "children_parent_idx", Columns: []string{"parent_id"}},
	}, table.Indexes)
}

// TestSQLiteInspectorRecordsPartialIndex proves that a SQLite partial index
// is now described rather than rejected: inspect used to fail the whole
// table on the first partial index, which would abort a sweep over a
// production schema the moment it reached one. The plain index alongside
// it keeps IndexDef.Predicate's zero value, proving the field only records
// a predicate when the index actually has one.
func TestSQLiteInspectorRecordsPartialIndex(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "database.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX users_id_idx ON users (id)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX users_active_idx ON users (status) WHERE status = 'active'")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_active_idx", Columns: []string{"status"}, Predicate: "status = 'active'"},
		{Name: "users_id_idx", Columns: []string{"id"}},
	}, table.Indexes)
}

// TestSQLiteInspectorRecordsExpressionIndex proves that a SQLite
// expression index, and one that mixes a plain column with an expression,
// are now described rather than rejected. The mixed index proves
// Expressions carries every key including the plain one, verbatim as its
// bare column name, and leaves Columns empty rather than splitting the key
// order across both fields.
func TestSQLiteInspectorRecordsExpressionIndex(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "database.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX users_lower_email_idx ON users (lower(email))")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE INDEX users_id_lower_email_idx ON users (id, lower(email))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{
		{Name: "users_id_lower_email_idx", Expressions: []string{"id", "lower(email)"}},
		{Name: "users_lower_email_idx", Expressions: []string{"lower(email)"}},
	}, table.Indexes)
}

// TestSQLiteInspectorRejectsUnrepresentableTableMetadata proves that
// inspect.Table still refuses the SQLite objects this package genuinely
// cannot describe: a view, which has no independent column, constraint, or
// index structure of its own for a TableDef to hold, unlike a virtual
// table or a shadow table, both of which TestSQLiteInspectorRecordsVirtualTable
// and TestSQLiteInspectorRecordsShadowTable now prove inspect describes.
func TestSQLiteInspectorRejectsUnrepresentableTableMetadata(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	for _, statement := range []string{
		"CREATE TABLE base (id INTEGER PRIMARY KEY)",
		"CREATE VIEW base_view AS SELECT id FROM base",
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
		{table: "base_view", want: `table kind "view" is unsupported`},
	} {
		t.Run(test.table, func(t *testing.T) {
			_, err := inspector.Table(t.Context(), test.table)
			require.ErrorContains(t, err, test.want)
		})
	}
}

// TestSQLiteInspectorRecordsStrictTable proves that a SQLite STRICT table
// is now described rather than rejected: STRICT and WITHOUT ROWID used to
// fail the whole table together, at the same check
// TestSQLiteInspectorRejectsUnrepresentableTableMetadata used to cover.
// Every column here uses one of STRICT's own allowed type names (INTEGER,
// TEXT) so the CREATE TABLE itself succeeds under SQLite's stricter column
// rules, and each also maps to a logical type rasql already models.
func TestSQLiteInspectorRecordsStrictTable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT) STRICT")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.True(t, table.Strict)
	require.False(t, table.WithoutRowID)
}

// TestSQLiteInspectorRecordsWithoutRowIDTable is the WithoutRowID
// counterpart to TestSQLiteInspectorRecordsStrictTable.
func TestSQLiteInspectorRecordsWithoutRowIDTable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT) WITHOUT ROWID")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.True(t, table.WithoutRowID)
	require.False(t, table.Strict)
}

// TestSQLiteInspectorRecordsPrimaryKeyAutoincrement proves that a SQLite
// primary key declared AUTOINCREMENT is now described rather than
// rejected, the AUTOINCREMENT counterpart to
// TestSQLiteInspectorRecordsStrictTable.
func TestSQLiteInspectorRecordsPrimaryKeyAutoincrement(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.True(t, table.PrimaryKeyAutoincrement)
	require.Equal(t, schema.ConflictResolution(""), table.PrimaryKeyOnConflict)
}

// TestSQLiteInspectorRecordsPrimaryKeyConflictResolution proves that a
// SQLite primary key naming an ON CONFLICT resolution, on either the
// column-level or table-level PRIMARY KEY form, is now described rather
// than rejected.
func TestSQLiteInspectorRecordsPrimaryKeyConflictResolution(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY ON CONFLICT REPLACE, name TEXT)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE orders (id INTEGER, name TEXT, PRIMARY KEY (id) ON CONFLICT IGNORE)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)

	users, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.False(t, users.PrimaryKeyAutoincrement)
	require.Equal(t, schema.ConflictReplace, users.PrimaryKeyOnConflict)

	orders, err := inspector.Table(t.Context(), "orders")
	require.NoError(t, err)
	require.False(t, orders.PrimaryKeyAutoincrement)
	require.Equal(t, schema.ConflictIgnore, orders.PrimaryKeyOnConflict)
}

// TestSQLiteInspectorRecordsDefaultPrimaryKeyConflictResolution proves that
// an explicit ON CONFLICT ABORT clause on a primary key, and an omitted
// one, both name schema.ConflictResolution's zero value, the same fold
// TestSQLiteInspectorRecordsDefaultConflictResolution proves for a unique
// constraint's ON CONFLICT clause.
func TestSQLiteInspectorRecordsDefaultPrimaryKeyConflictResolution(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY ON CONFLICT ABORT, name TEXT)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "users")
	require.NoError(t, err)
	require.Equal(t, schema.ConflictResolution(""), table.PrimaryKeyOnConflict)
}

// TestSQLiteInspectorRecordsGeneratedColumns covers both SQLite generated
// column storage kinds against a real table: STORED, which SQLite writes
// into the table, and VIRTUAL (both the explicit spelling and the implicit
// one a bare "AS (expr)" states), which SQLite computes each time the
// column is read. Before this feature existed, PRAGMA table_xinfo's hidden
// flag made the whole table unrepresentable the moment it carried any
// generated column, which is the exact failure this test proves no longer
// happens.
func TestSQLiteInspectorRecordsGeneratedColumns(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), `CREATE TABLE measurements (
		id INTEGER PRIMARY KEY,
		celsius INTEGER,
		fahrenheit_stored INTEGER GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED,
		fahrenheit_virtual INTEGER GENERATED ALWAYS AS (celsius * 9 / 5 + 32) VIRTUAL,
		fahrenheit_implicit INTEGER AS (celsius * 9 / 5 + 32)
	)`)
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "measurements")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "celsius", Type: schema.IntegerType{}, Nullable: true},
		{
			Name:                "fahrenheit_stored",
			Type:                schema.IntegerType{},
			Nullable:            true,
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedStored,
		},
		{
			Name:                "fahrenheit_virtual",
			Type:                schema.IntegerType{},
			Nullable:            true,
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedVirtual,
		},
		{
			Name:                "fahrenheit_implicit",
			Type:                schema.IntegerType{},
			Nullable:            true,
			GeneratedExpression: "celsius * 9 / 5 + 32",
			GeneratedStorage:    schema.GeneratedVirtual,
		},
	}, table.Columns)

	// render still refuses DDL for a generated column until it can build a
	// GENERATED ALWAYS AS clause; see TestCreateTableRejectsGeneratedColumn
	// in the render package for that refusal.
	_, err = render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"fahrenheit_stored"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestSQLiteInspectorRecordsVirtualTable proves that a live SQLite FTS5
// virtual table is now described instead of rejecting the whole sweep the
// moment it is reached: TableDef.VirtualTableModule and
// .VirtualTableModuleArguments record the module and its arguments, and
// the module's own hidden columns — FTS5 exposes one named after the
// table itself, used for MATCH filtering, and one named "rank" — are
// recorded as ordinary ColumnDefs with Hidden set, rather than failing
// inspection the way a genuinely hidden column used to unconditionally.
// The bundled modernc.org/sqlite build used by this package's tests
// includes FTS5; TestSQLiteInspectorRecordsVirtualTableFromLegacyKind
// covers a virtual table using the always-available rtree module instead,
// for a build where FTS5 is unavailable.
func TestSQLiteInspectorRecordsVirtualTable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE VIRTUAL TABLE posts_fts USING fts5(body, tokenize='porter')")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "posts_fts")
	require.NoError(t, err)
	require.Equal(t, "fts5", table.VirtualTableModule)
	require.Equal(t, []string{"body", "tokenize='porter'"}, table.VirtualTableModuleArguments)
	require.Empty(t, table.PrimaryKey)
	require.Equal(t, []schema.ColumnDef{
		{Name: "body", Type: schema.BytesType{}, Nullable: true},
		{Name: "posts_fts", Type: schema.BytesType{}, Nullable: true, Hidden: true},
		{Name: "rank", Type: schema.BytesType{}, Nullable: true, Hidden: true},
	}, table.Columns)

	// render still refuses DDL for a virtual table until it can build a
	// CREATE VIRTUAL TABLE statement; see TestCreateTableRejectsVirtualTable
	// in the render package for that refusal.
	_, err = render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"posts_fts"`)
	require.ErrorContains(t, err, "can describe but not yet render")
}

// TestSQLiteInspectorRecordsShadowTable proves that a virtual table
// module's own backing table — R-Tree's own "_node" table here — is
// described the same way an ordinary table is, rather than rejected for
// its PRAGMA table_list kind, "shadow": a shadow table's own CREATE TABLE
// text is an entirely ordinary table definition with no virtual-table
// facts of its own. R-Tree is used here rather than FTS5 so this test does
// not depend on the single-quoted table name FTS5's own shadow tables use
// in their CREATE TABLE text; TestSQLiteInspectorRecordsFTS5ShadowTables
// covers that form specifically.
func TestSQLiteInspectorRecordsShadowTable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE VIRTUAL TABLE shapes USING rtree(id, minx, maxx, miny, maxy)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "shapes_node")
	require.NoError(t, err)
	require.Empty(t, table.VirtualTableModule)
	require.Equal(t, []string{"nodeno"}, table.PrimaryKey)
	require.Equal(t, []schema.ColumnDef{
		{Name: "nodeno", Type: schema.IntegerType{}},
		{Name: "data", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
}

// TestSQLiteInspectorRecordsFTS5ShadowTables proves that every table an
// fts5 virtual table creates — the virtual table itself and its five shadow
// tables — describes successfully against the embedded modernc.org/sqlite
// driver actually used at runtime, not only against a hand-written sqlmock
// fixture. FTS5 persists each shadow table's own CREATE TABLE text with its
// table name single-quoted (sqlite_master.sql for the "_data" table, for
// instance, reads CREATE TABLE 'posts_fts_data'(...)), a form
// rasql-sqlite's parser previously rejected.
func TestSQLiteInspectorRecordsFTS5ShadowTables(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE VIRTUAL TABLE posts_fts USING fts5(title, body)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)

	table, err := inspector.Table(t.Context(), "posts_fts")
	require.NoError(t, err)
	require.Equal(t, "fts5", table.VirtualTableModule)

	for _, shadowTable := range []string{"posts_fts_data", "posts_fts_idx", "posts_fts_content", "posts_fts_docsize", "posts_fts_config"} {
		shadow, err := inspector.Table(t.Context(), shadowTable)
		require.NoErrorf(t, err, "Table(%q)", shadowTable)
		require.Emptyf(t, shadow.VirtualTableModule, "Table(%q).VirtualTableModule", shadowTable)
		require.NotEmptyf(t, shadow.Columns, "Table(%q).Columns", shadowTable)
	}
}

// TestSQLiteInspectorSweepsFTS5Database proves the user-visible point of the
// single-quoted-table-name parser fix: TableNames, the same enumeration a
// full rasqlgen sweep drives, lists an fts5 virtual table's shadow tables
// alongside the virtual table itself, and every one of them describes
// without error, so a sweep over a database that uses full-text search now
// completes.
func TestSQLiteInspectorSweepsFTS5Database(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	_, err = database.ExecContext(t.Context(), "CREATE VIRTUAL TABLE posts_fts USING fts5(title, body)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)

	names, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Len(t, names, 6) // posts_fts itself plus its five FTS5 shadow tables

	for _, name := range names {
		_, err := inspector.Table(t.Context(), name.Name)
		require.NoErrorf(t, err, "Table(%q)", name.Name)
	}
}

func TestSQLiteInspectorMarksTableLevelIntegerPrimaryKeysAsNonNullable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events_asc (id INTEGER, payload BLOB, PRIMARY KEY (id ASC))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events_asc")
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.False(t, table.Columns[0].Nullable)
}

func TestSQLiteInspectorMarksTableLevelDescendingIntegerPrimaryKeyAsNonNullable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events (id INTEGER, payload BLOB, PRIMARY KEY (id DESC))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}, Nullable: true},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

func TestSQLiteInspectorMarksTableLevelCollatedIntegerPrimaryKeyAsNonNullable(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events (id INTEGER, payload BLOB, PRIMARY KEY (id COLLATE NOCASE))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}, Nullable: true},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

func TestSQLiteInspectorAcceptsQuotedIntegerPrimaryKeyType(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), `CREATE TABLE events (id "INTEGER" PRIMARY KEY, payload BLOB)`)
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.False(t, table.Columns[0].Nullable)
}

func TestSQLiteInspectorUsesSelectedTempSchemaCatalog(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE main.Events (id TEXT PRIMARY KEY, payload BLOB)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TEMP TABLE Events (id INTEGER PRIMARY KEY, payload BLOB)")
	require.NoError(t, err)

	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inspector, err := inspect.New(connection, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.TableIn(t.Context(), "temp", "events")
	require.NoError(t, err)
	require.Equal(t, schema.IntegerType{}, table.Columns[0].Type)
	require.False(t, table.Columns[0].Nullable)
}

func TestSQLiteInspectorUsesSelectedAttachedSchemaCatalog(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS tenant")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE tenant.Events (id INTEGER, payload BLOB, PRIMARY KEY (id DESC))")
	require.NoError(t, err)

	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inspector, err := inspect.New(connection, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.TableIn(t.Context(), "tenant", "events")
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.True(t, table.Columns[0].Nullable)
}

func TestSQLiteInspectorMatchesMainTableNamesCaseInsensitively(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE Events (id INTEGER PRIMARY KEY, payload BLOB)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.False(t, table.Columns[0].Nullable)
}

func TestSQLiteInspectorKeepsDeclarationLookupOnOneConnection(t *testing.T) {
	driverInstance := &sqliteAffinityDriver{}
	driverName := "rasql-inspect-connection-affinity-" + strconv.FormatInt(sqliteAffinityDriverNames.Add(1), 10)
	sql.Register(driverName, driverInstance)

	database, err := sql.Open(driverName, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.False(t, table.Columns[0].Nullable)
	require.Equal(t, int64(1), driverInstance.connections.Load())
}

// TestSQLiteInspectorRecordsVirtualTableFromLegacyKind proves that a
// virtual table is described even when its own PRAGMA table_list row
// reports kind "table" rather than "virtual" — the shape a pre-3.37
// sqlite_master-based fallback (or any other engine that predates
// table_list's own virtual/shadow kinds) reports for one, since
// sqlite_master has no separate virtual type. inspect detects a virtual
// table from its own CREATE VIRTUAL TABLE definition text instead of
// trusting kind, so this table is still recognized and described exactly
// as TestSQLiteInspectorRecordsVirtualTable proves for a live one whose
// kind PRAGMA table_list correctly reports as "virtual".
func TestSQLiteInspectorRecordsVirtualTableFromLegacyKind(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery(`PRAGMA table_list("virtual_table")`).
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}).
			AddRow("main", "virtual_table", "table", 5, 0, 0))
	mock.ExpectQuery(`PRAGMA "main".table_xinfo("virtual_table")`).
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "id", "INTEGER", 0, nil, 0, 0).
			AddRow(1, "minx", "REAL", 0, nil, 0, 0).
			AddRow(2, "maxx", "REAL", 0, nil, 0, 0).
			AddRow(3, "miny", "REAL", 0, nil, 0, 0).
			AddRow(4, "maxy", "REAL", 0, nil, 0, 0))
	mock.ExpectQuery("SELECT sql FROM \"main\".sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("virtual_table").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).
			AddRow("CREATE VIRTUAL TABLE virtual_table USING rtree(id, minx, maxx, miny, maxy)"))
	mock.ExpectQuery(`PRAGMA "main".index_list("virtual_table")`).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}))
	mock.ExpectQuery(`PRAGMA "main".foreign_key_list("virtual_table")`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}))

	table, err := inspector.Table(t.Context(), "virtual_table")
	require.NoError(t, err)
	require.Equal(t, "rtree", table.VirtualTableModule)
	require.Equal(t, []string{"id", "minx", "maxx", "miny", "maxy"}, table.VirtualTableModuleArguments)
}

var sqliteAffinityDriverNames atomic.Int64

type sqliteAffinityDriver struct {
	connections atomic.Int64
}

func (d *sqliteAffinityDriver) Open(string) (driver.Conn, error) {
	return &sqliteAffinityConn{hasTable: d.connections.Add(1) == 1}, nil
}

type sqliteAffinityConn struct {
	hasTable bool
	queries  atomic.Int64
}

func (c *sqliteAffinityConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (c *sqliteAffinityConn) Close() error {
	return nil
}

func (c *sqliteAffinityConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (c *sqliteAffinityConn) IsValid() bool {
	return c.queries.Load() == 0
}

func (c *sqliteAffinityConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	switch query {
	case `PRAGMA table_list("events")`:
		if !c.hasTable {
			return &sqliteAffinityRows{columns: []string{"schema", "name", "type", "ncol", "wr", "strict"}}, nil
		}
		return &sqliteAffinityRows{
			columns: []string{"schema", "name", "type", "ncol", "wr", "strict"},
			values:  [][]driver.Value{{"main", "Events", "table", int64(1), int64(0), int64(0)}},
		}, nil
	case `PRAGMA "main".table_xinfo("Events")`:
		if !c.hasTable {
			return &sqliteAffinityRows{columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}}, nil
		}
		return &sqliteAffinityRows{
			columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"},
			values:  [][]driver.Value{{int64(0), "id", "INTEGER", int64(0), nil, int64(1), int64(0)}},
		}, nil
	case `SELECT sql FROM "main".sqlite_master WHERE type = 'table' AND name = ?`:
		return &sqliteAffinityRows{columns: []string{"sql"}, values: [][]driver.Value{{`CREATE TABLE Events (id INTEGER PRIMARY KEY, payload BLOB)`}}}, nil
	case `PRAGMA "main".index_list("Events")`:
		return &sqliteAffinityRows{columns: []string{"seq", "name", "unique", "origin", "partial"}}, nil
	case `PRAGMA "main".foreign_key_list("Events")`:
		return &sqliteAffinityRows{columns: []string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}}, nil
	default:
		return nil, errors.New("unexpected SQLite affinity query: " + query)
	}
}

type sqliteAffinityRows struct {
	columns  []string
	values   [][]driver.Value
	position int
}

func (r *sqliteAffinityRows) Columns() []string {
	return r.columns
}

func (r *sqliteAffinityRows) Close() error {
	return nil
}

func (r *sqliteAffinityRows) Next(values []driver.Value) error {
	if r.position == len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.position])
	r.position++
	return nil
}

func TestSQLiteInspectorPreservesNullableCompositeIntegerPrimaryKey(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events (id INTEGER, sequence INTEGER, payload BLOB, PRIMARY KEY (id, sequence))")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}, Nullable: true},
		{Name: "sequence", Type: schema.IntegerType{}, Nullable: true},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id", "sequence"}, table.PrimaryKey)
}

func TestSQLiteInspectorPreservesNullableDescendingIntegerPrimaryKey(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events (id INTEGER PRIMARY KEY DESC, payload BLOB)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}, Nullable: true},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

func TestSQLiteInspectorPreservesNullableTextPrimaryKey(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE events (id TEXT PRIMARY KEY, payload BLOB)")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.TextType{}, Nullable: true},
		{Name: "payload", Type: schema.BytesType{}, Nullable: true},
	}, table.Columns)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

// nilPointerDialect is a stub dialect.Dialect implemented with pointer
// receivers, so a typed-nil *nilPointerDialect value is a non-nil interface
// value that would panic if dereferenced.
type nilPointerDialect struct{}

func (*nilPointerDialect) Name() string { return "stub" }

func (*nilPointerDialect) QuoteIdentifier(string) (string, error) { return "", nil }

func (*nilPointerDialect) Placeholder(int) (string, error) { return "", nil }

func (*nilPointerDialect) TypeName(schema.ColumnDef) (string, error) { return "", nil }

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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}))
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}))
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}))
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("name", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 3)

	table, err := inspector.Table(t.Context(), "widgets")
	require.Error(t, err)
	require.Equal(t, schema.TableDef{}, table)
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("name", "character varying", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
	expectPostgreSQLCatalogColumnCount(mock, "widgets", 2)
	mock.ExpectQuery("SELECT attribute\\.attname FROM pg_catalog\\.pg_constraint").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"attname"}).AddRow("id"))
	expectPostgreSQLEmptyMetadata(mock, "widgets")

	table, err := inspector.Table(t.Context(), "widgets")
	require.NoError(t, err)
	require.Equal(t, []schema.ColumnDef{
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("widgets").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
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
	mock.ExpectQuery("SELECT column_data.column_name, column_data.data_type, column_data.is_nullable, column_data.column_default, column_data.numeric_precision, column_data.numeric_scale, column_data.character_maximum_length, column_data.is_generated, column_data.generation_expression, attribute.attgenerated FROM information_schema\\.columns").
		WithArgs("memberships").
		WillReturnRows(sqlmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "numeric_precision", "numeric_scale", "character_maximum_length", "is_generated", "generation_expression", "attgenerated"}).
			AddRow("tenant_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("account_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, "").
			AddRow("user_id", "bigint", "NO", nil, nil, nil, nil, "NEVER", nil, ""))
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
	mock.ExpectQuery("PRAGMA database_list").
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
	mock.ExpectQuery(`SELECT name, type FROM "main".sqlite_master WHERE name = ? COLLATE NOCASE AND type IN ('table', 'view')`).
		WithArgs("ghosts").
		WillReturnRows(sqlmock.NewRows([]string{"name", "type"}))

	_, err = inspector.Table(t.Context(), "ghosts")
	require.Error(t, err)
	require.ErrorIs(t, err, inspect.ErrTableNotFound)
	var notFound *inspect.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, "ghosts", notFound.Table)
	require.Contains(t, err.Error(), `"ghosts"`)
	require.NotContains(t, err.Error(), "normalize table")
}

func TestSQLiteInspectorFallsBackWhenTableListIsUnavailable(t *testing.T) {
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
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}))
	mock.ExpectQuery("PRAGMA database_list").
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
	mock.ExpectQuery(`SELECT name, type FROM "main".sqlite_master WHERE name = ? COLLATE NOCASE AND type IN ('table', 'view')`).
		WithArgs("events").
		WillReturnRows(sqlmock.NewRows([]string{"name", "type"}).AddRow("Events", "table"))
	mock.ExpectQuery("PRAGMA \"main\".table_xinfo(\"Events\")").
		WillReturnRows(sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}).
			AddRow(0, "id", "INTEGER", 1, nil, 1, 0))
	mock.ExpectQuery("SELECT sql FROM \"main\".sqlite_master WHERE type = 'table' AND name = ?").
		WithArgs("Events").
		WillReturnRows(sqlmock.NewRows([]string{"sql"}).AddRow("CREATE TABLE Events (id INTEGER PRIMARY KEY)"))
	mock.ExpectQuery(`PRAGMA "main".index_list("Events")`).
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}))
	mock.ExpectQuery(`PRAGMA "main".foreign_key_list("Events")`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}))

	table, err := inspector.Table(t.Context(), "events")
	require.NoError(t, err)
	require.Equal(t, "Events", table.Name)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
}

// TestPostgreSQLInspectorReadsTableNames covers TableNames' pg_catalog
// query: it must scope to current_schema(), sort the returned rows, and
// leave every TableName.Schema empty (see the TableName doc comment). The
// same shape is covered for MySQL by TestMySQLInspectorReadsTableNames and
// confirmed against a real server by
// TestPostgreSQLInspectorReadsTableNamesAgainstLiveDatabase.
func TestPostgreSQLInspectorReadsTableNames(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT table_data\\.relname FROM pg_catalog\\.pg_class.*relkind IN \\('r','p'\\)").
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).
			AddRow("zebras").
			AddRow("armadillos"))

	refs, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Name: "armadillos"}, {Name: "zebras"}}, refs)
}

// TestMySQLInspectorReadsTableNames covers TableNames' information_schema
// query for MySQL: it must scope to DATABASE(), filter to table_type =
// 'BASE TABLE', sort the returned rows, and leave every TableName.Schema
// empty.
func TestMySQLInspectorReadsTableNames(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.MySQL())
	require.NoError(t, err)
	mock.ExpectQuery("SELECT table_name FROM information_schema\\.tables WHERE table_schema = DATABASE\\(\\) AND table_type = 'BASE TABLE'").
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).
			AddRow("zebras").
			AddRow("armadillos"))

	refs, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Name: "armadillos"}, {Name: "zebras"}}, refs)
}

// TestSQLiteInspectorTableNamesFallsBackWhenTableListIsUnavailable mirrors
// TestSQLiteInspectorFallsBackWhenTableListIsUnavailable for TableNames:
// when PRAGMA table_list yields no rows, sqliteLegacyTableNames walks
// PRAGMA database_list and each database's sqlite_master, filling
// TableName.Schema with the database each row came from.
func TestSQLiteInspectorTableNamesFallsBackWhenTableListIsUnavailable(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list").
		WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}))
	mock.ExpectQuery("PRAGMA database_list").
		WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
	mock.ExpectQuery(`SELECT name FROM "main".sqlite_master WHERE type = 'table'`).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).
			AddRow("zebras").
			AddRow("sqlite_sequence").
			AddRow("armadillos"))

	refs, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "main", Name: "armadillos"}, {Schema: "main", Name: "zebras"}}, refs)
}

// TestSQLiteInspectorReadsTableNames uses a real in-memory SQLite database,
// like the other SQLite tests that exercise PRAGMA table_list end to end
// (e.g. TestSQLiteInspectorMatchesMainTableNamesCaseInsensitively), because
// SQLite needs no live server. It proves a view and SQLite's own internal
// sqlite_sequence table (created here by an AUTOINCREMENT column) are both
// excluded, and that the surviving rows are sorted and scoped to "main".
func TestSQLiteInspectorReadsTableNames(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "CREATE TABLE zebras (id INTEGER PRIMARY KEY AUTOINCREMENT)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE armadillos (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE VIEW zebra_view AS SELECT id FROM zebras")
	require.NoError(t, err)

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	refs, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "main", Name: "armadillos"}, {Schema: "main", Name: "zebras"}}, refs)
}

// TestSQLiteInspectorReadsTableNamesAcrossAttachedDatabases confirms
// TableNames' default scope matches Table's own default: main, temp, and
// every attached database, not only main, with each TableName.Schema naming
// which database a table came from.
func TestSQLiteInspectorReadsTableNamesAcrossAttachedDatabases(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS tenant")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE main.zebras (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE tenant.armadillos (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inspector, err := inspect.New(connection, dialect.SQLite())
	require.NoError(t, err)
	refs, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "main", Name: "zebras"}, {Schema: "tenant", Name: "armadillos"}}, refs)
}

// TestSQLiteInspectorReadsTableNamesDistinguishesDuplicateNamesAcrossDatabases
// is the scenario TableName exists for: the same table name in two SQLite
// databases would collapse into an indistinguishable duplicate if TableNames
// returned bare strings. TableName.Schema keeps the two apart.
func TestSQLiteInspectorReadsTableNamesDistinguishesDuplicateNamesAcrossDatabases(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS tenant")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE main.users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE tenant.users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inspector, err := inspect.New(connection, dialect.SQLite())
	require.NoError(t, err)
	refs, err := inspector.TableNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "main", Name: "users"}, {Schema: "tenant", Name: "users"}}, refs)
}

// TestSQLiteInspectorReadsTableNamesIn confirms TableNamesIn scopes to one
// named database, the enumeration counterpart of TableIn, with every
// returned TableName.Schema equal to the requested database name.
func TestSQLiteInspectorReadsTableNamesIn(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	_, err = database.ExecContext(t.Context(), "ATTACH DATABASE ':memory:' AS tenant")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE main.zebras (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE tenant.armadillos (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inspector, err := inspect.New(connection, dialect.SQLite())
	require.NoError(t, err)

	mainRefs, err := inspector.TableNamesIn(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "main", Name: "zebras"}}, mainRefs)

	tenantRefs, err := inspector.TableNamesIn(t.Context(), "tenant")
	require.NoError(t, err)
	require.Equal(t, []inspect.TableName{{Schema: "tenant", Name: "armadillos"}}, tenantRefs)
}

// TestSQLiteInspectorTableNamesInRequiresRetainedConnectionForAttachedDatabase
// mirrors sqliteTable's own retained-connection requirement: an attached (or
// temp) database's ATTACH state lives on one physical connection, so a plain
// *sql.DB pool handle cannot be trusted to reach it.
func TestSQLiteInspectorTableNamesInRequiresRetainedConnectionForAttachedDatabase(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = inspector.TableNamesIn(t.Context(), "tenant")
	require.ErrorContains(t, err, "retained")
}
