package render_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestCreateTableRendersDialectTypesAndConstraints(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "metadata", Type: schema.JSONType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:    "orders_customer_key",
			Columns: []string{"customer_id"},
		}},
		Checks: []schema.CheckDef{{
			Name:       "orders_id_check",
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
		Indexes: []schema.IndexDef{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
	}

	rendered, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE \"orders\" (\"id\" BIGINT NOT NULL, \"customer_id\" BIGINT NOT NULL, \"metadata\" JSONB, PRIMARY KEY (\"id\"), CONSTRAINT \"orders_customer_key\" UNIQUE (\"customer_id\"), CONSTRAINT \"orders_id_check\" CHECK (id > 0), CONSTRAINT \"orders_customer_fk\" FOREIGN KEY (\"customer_id\") REFERENCES \"customers\" (\"id\") ON DELETE CASCADE)", rendered.SQL())
	require.Empty(t, rendered.Args())

	indexes, err := render.CreateIndexes(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Equal(t, []string{"CREATE INDEX `orders_customer_idx` ON `orders` (`customer_id`)"}, sqls(indexes))
}

func TestCreateTableRendersDecimalColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `CREATE TABLE "invoices" ("id" BIGINT NOT NULL, "amount" NUMERIC(19,4) NOT NULL, "tax_rate" NUMERIC(5,4), PRIMARY KEY ("id"))`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "CREATE TABLE `invoices` (`id` BIGINT NOT NULL, `amount` DECIMAL(19,4) NOT NULL, `tax_rate` DECIMAL(5,4), PRIMARY KEY (`id`))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `CREATE TABLE "invoices" ("id" INTEGER NOT NULL, "amount" TEXT NOT NULL, "tax_rate" TEXT, PRIMARY KEY ("id"))`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Empty(t, rendered.Args())
		})
	}
}

// TestCreateTableRendersUnsignedIntegerColumns is the DDL half of the fix for
// an unsigned integer column: MySQL keeps the UNSIGNED the descriptor states,
// where it used to render a signed BIGINT that stops at 9223372036854775807,
// and the two dialects with no unsigned integer type refuse the table instead
// of narrowing it silently.
func TestCreateTableRendersUnsignedIntegerColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "sequence", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	rendered, err := render.CreateTable(dialect.MySQL(), table)
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE `events` (`id` BIGINT UNSIGNED NOT NULL, `sequence` BIGINT NOT NULL, PRIMARY KEY (`id`))", rendered.SQL())

	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := render.CreateTable(d, table)
			require.ErrorContains(t, err, `column "id"`)
			require.ErrorContains(t, err, "has no unsigned integer type")
		})
	}
}

// TestCreateTableRendersTextWidthColumns is the DDL half of the text-width
// fix: MySQL renders a stated width as VARCHAR(width), where it used to
// render every schema.TextType column TEXT regardless, which MySQL refuses
// to index without a key length (error 1170). PostgreSQL renders
// VARCHAR(n) too, rejecting an over-length insert whatever the server
// settings where MySQL only does so under strict SQL mode, so it renders
// the same way; SQLite renders plain TEXT regardless of a stated width,
// since it assigns column storage by affinity rather than by declared type
// and would not enforce the bound either way.
func TestCreateTableRendersTextWidthColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}},
			{Name: "bio", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `CREATE TABLE "users" ("id" BIGINT NOT NULL, "email" VARCHAR(255) NOT NULL, "bio" TEXT, PRIMARY KEY ("id"))`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "CREATE TABLE `users` (`id` BIGINT NOT NULL, `email` VARCHAR(255) NOT NULL, `bio` TEXT, PRIMARY KEY (`id`))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `CREATE TABLE "users" ("id" INTEGER NOT NULL, "email" TEXT NOT NULL, "bio" TEXT, PRIMARY KEY ("id"))`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
		})
	}
}

// TestCreateTableRejectsUnboundedMySQLTextInPrimaryKeyOrUnique covers the
// render-time refusal this change adds for MySQL: an unbounded (no stated
// width) TextType column used as a primary key or a unique constraint would
// otherwise render CREATE TABLE SQL MySQL itself rejects with error 1170
// ("BLOB/TEXT column used in key specification without a key length"). The
// error names the column and points at schema.Width instead of surfacing
// that opaque server error.
func TestCreateTableRejectsUnboundedMySQLTextInPrimaryKeyOrUnique(t *testing.T) {
	t.Run("primary key", func(t *testing.T) {
		table := schema.TableDef{
			Name:       "slugs",
			Columns:    []schema.ColumnDef{{Name: "slug", Type: schema.TextType{}}},
			PrimaryKey: []string{"slug"},
		}
		_, err := render.CreateTable(dialect.MySQL(), table)
		require.ErrorContains(t, err, `column "slug" has no stated width`)
		require.ErrorContains(t, err, "schema.Width")
		require.ErrorContains(t, err, "a primary key")
	})

	t.Run("unique constraint", func(t *testing.T) {
		table := schema.TableDef{
			Name: "slugs",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "alt", Type: schema.TextType{}},
			},
			PrimaryKey:        []string{"id"},
			UniqueConstraints: []schema.UniqueDef{{Columns: []string{"alt"}}},
		}
		_, err := render.CreateTable(dialect.MySQL(), table)
		require.ErrorContains(t, err, `column "alt" has no stated width`)
		require.ErrorContains(t, err, "schema.Width")
		require.ErrorContains(t, err, "a unique constraint")
	})

	t.Run("a stated width renders instead of refusing", func(t *testing.T) {
		table := schema.TableDef{
			Name:       "slugs",
			Columns:    []schema.ColumnDef{{Name: "slug", Type: schema.TextType{Width: schema.NewTextWidth(255)}}},
			PrimaryKey: []string{"slug"},
		}
		rendered, err := render.CreateTable(dialect.MySQL(), table)
		require.NoError(t, err)
		require.Equal(t, "CREATE TABLE `slugs` (`slug` VARCHAR(255) NOT NULL, PRIMARY KEY (`slug`))", rendered.SQL())
	})

	t.Run("postgresql and sqlite index an unbounded text primary key natively", func(t *testing.T) {
		table := schema.TableDef{
			Name:       "slugs",
			Columns:    []schema.ColumnDef{{Name: "slug", Type: schema.TextType{}}},
			PrimaryKey: []string{"slug"},
		}
		for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
			t.Run(d.Name(), func(t *testing.T) {
				_, err := render.CreateTable(d, table)
				require.NoError(t, err)
			})
		}
	})
}

// TestCreateIndexRejectsUnboundedMySQLText is the CREATE INDEX counterpart
// to TestCreateTableRejectsUnboundedMySQLTextInPrimaryKeyOrUnique: MySQL
// refuses a secondary index over an unbounded text column with the same
// error 1170, and render.CreateIndexes now refuses it first with a message
// naming the column and schema.Width.
func TestCreateIndexRejectsUnboundedMySQLText(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes:    []schema.IndexDef{{Name: "idx_documents_title", Columns: []string{"title"}}},
	}

	_, err := render.CreateIndexes(dialect.MySQL(), table)
	require.ErrorContains(t, err, `column "title" has no stated width`)
	require.ErrorContains(t, err, "schema.Width")
	require.ErrorContains(t, err, "an index")

	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		t.Run(d.Name(), func(t *testing.T) {
			_, err := render.CreateIndexes(d, table)
			require.NoError(t, err)
		})
	}

	widened := table
	widened.Columns = []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "title", Type: schema.TextType{Width: schema.NewTextWidth(255)}},
	}
	indexes, err := render.CreateIndexes(dialect.MySQL(), widened)
	require.NoError(t, err)
	require.Equal(t, []string{"CREATE INDEX `idx_documents_title` ON `documents` (`title`)"}, sqls(indexes))
}

// TestCreateIndexesRejectsNonDefaultMethod proves that an IndexDef naming a
// non-default schema.IndexMethod, such as a GIN index inspect now describes
// instead of rejecting, is refused at render time with a typed error rather
// than silently rendered as a plain default index: rasql does not yet know
// how to build DDL for anything other than a plain default index, and
// rendering a B-tree in a GIN index's place would be the exact wrong-DDL
// failure this refusal exists to prevent.
func TestCreateIndexesRejectsNonDefaultMethod(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "tags", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "documents_tags_gin_idx",
			Columns: []string{"tags"},
			Method:  "gin",
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_tags_gin_idx"`)
	require.ErrorContains(t, err, `"gin"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var methodErr *render.UnsupportedIndexMethodError
	require.ErrorAs(t, err, &methodErr)
	require.Equal(t, "documents_tags_gin_idx", methodErr.Index)
	require.Equal(t, schema.IndexMethod("gin"), methodErr.Method)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexMethod)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)

	// CreateTable never reaches an index's Method: indexes render as
	// separate CREATE INDEX statements, never inline in CREATE TABLE, so
	// the same table's non-default method does not stop it from rendering.
	_, err = render.CreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
}

// TestCreateIndexesRejectsPartialIndex proves that an IndexDef naming a
// Predicate, such as a partial index inspect now describes instead of
// rejecting, is refused at render time with a typed error rather than
// silently rendered as a plain unconditional index: rendering a plain index
// in a partial index's place would build a stricter index than the one the
// database actually has, which is the exact wrong-DDL failure this refusal
// exists to prevent.
func TestCreateIndexesRejectsPartialIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "documents_active_idx",
			Columns:   []string{"status"},
			Predicate: "status = 'active'",
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_active_idx"`)
	require.ErrorContains(t, err, `"status = 'active'"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var predicateErr *render.UnsupportedPartialIndexError
	require.ErrorAs(t, err, &predicateErr)
	require.Equal(t, "documents_active_idx", predicateErr.Index)
	require.Equal(t, "status = 'active'", predicateErr.Predicate)
	require.ErrorIs(t, err, render.ErrUnsupportedPartialIndex)

	// CreateTable never reaches an index's Predicate: indexes render as
	// separate CREATE INDEX statements, never inline in CREATE TABLE, so
	// the same table's partial index does not stop it from rendering.
	_, err = render.CreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
}

// TestCreateIndexesRejectsExpressionIndex proves that an IndexDef naming
// Expressions instead of Columns, such as an expression index inspect now
// describes instead of rejecting, is refused at render time with a typed
// error rather than silently rendered over the wrong columns: rasql does
// not yet know how to build DDL for an expression key.
func TestCreateIndexesRejectsExpressionIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:        "documents_lower_title_idx",
			Expressions: []string{"lower(title)"},
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_lower_title_idx"`)
	require.ErrorContains(t, err, "lower(title)")
	require.ErrorContains(t, err, "can describe but not yet render")

	var expressionErr *render.UnsupportedExpressionIndexError
	require.ErrorAs(t, err, &expressionErr)
	require.Equal(t, "documents_lower_title_idx", expressionErr.Index)
	require.Equal(t, []string{"lower(title)"}, expressionErr.Expressions)
	require.ErrorIs(t, err, render.ErrUnsupportedExpressionIndex)
}

// TestCreateIndexesRejectsIncludeColumns proves that an IndexDef naming
// IncludeColumns, such as a PostgreSQL INCLUDE index inspect now describes
// instead of rejecting, is refused at render time with a typed error rather
// than silently rendered without its covering columns: rasql does not yet
// know how to build DDL for an INCLUDE clause.
func TestCreateIndexesRejectsIncludeColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
			{Name: "title", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:           "documents_status_idx",
			Columns:        []string{"status"},
			IncludeColumns: []string{"title"},
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, `"title"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var includeErr *render.UnsupportedIndexIncludeColumnsError
	require.ErrorAs(t, err, &includeErr)
	require.Equal(t, "documents_status_idx", includeErr.Index)
	require.Equal(t, []string{"title"}, includeErr.IncludeColumns)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexIncludeColumns)
}

// TestCreateIndexesRejectsInvisibleIndex proves that an IndexDef setting
// Invisible, such as a MySQL invisible index inspect now describes instead
// of rejecting, is refused at render time with a typed error rather than
// silently rendered as a visible index: rasql does not yet know how to
// build DDL for an INVISIBLE index.
func TestCreateIndexesRejectsInvisibleIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "documents_status_idx",
			Columns:   []string{"status"},
			Invisible: true,
		}},
	}

	_, err := render.CreateIndexes(dialect.MySQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, "invisible")
	require.ErrorContains(t, err, "can describe but not yet render")

	var invisibleErr *render.UnsupportedIndexInvisibleError
	require.ErrorAs(t, err, &invisibleErr)
	require.Equal(t, "documents_status_idx", invisibleErr.Index)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexInvisible)
}

// TestCreateIndexesRejectsKeyDetails proves that an IndexDef naming Keys,
// such as a descending or non-default-collation key inspect now describes
// instead of rejecting, is refused at render time with a typed error rather
// than silently rendered as a plain ascending key: rasql does not yet know
// how to build DDL for a DESC key, a non-default collation or operator
// class, or a MySQL prefix part.
func TestCreateIndexesRejectsKeyDetails(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name: "documents_created_at_idx",
			Keys: []schema.IndexKeyDef{{Expression: "created_at", Descending: true}},
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_created_at_idx"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var keysErr *render.UnsupportedIndexKeyDetailsError
	require.ErrorAs(t, err, &keysErr)
	require.Equal(t, "documents_created_at_idx", keysErr.Index)
	require.Equal(t, []schema.IndexKeyDef{{Expression: "created_at", Descending: true}}, keysErr.Keys)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexKeyDetails)
}

// TestCreateIndexesRejectsNotValidIndex proves that an IndexDef setting
// NotValid, such as an index left behind by a failed CREATE INDEX
// CONCURRENTLY that inspect now describes instead of rejecting, is refused
// at render time with a typed error rather than silently rendered as a
// plain, usable index.
func TestCreateIndexesRejectsNotValidIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:     "documents_status_idx",
			Columns:  []string{"status"},
			NotValid: true,
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, "not valid")
	require.ErrorContains(t, err, "can describe but not yet render")

	var notValidErr *render.UnsupportedIndexNotValidError
	require.ErrorAs(t, err, &notValidErr)
	require.Equal(t, "documents_status_idx", notValidErr.Index)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexNotValid)
}

// TestCreateIndexesRejectsStorageParameters proves that an IndexDef naming
// StorageParameters, such as a fillfactor inspect now describes instead of
// rejecting, is refused at render time with a typed error rather than
// silently rendered without its WITH (...) clause.
func TestCreateIndexesRejectsStorageParameters(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:              "documents_status_idx",
			Columns:           []string{"status"},
			StorageParameters: map[string]string{"fillfactor": "70"},
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, "storage parameters")
	require.ErrorContains(t, err, "can describe but not yet render")

	var storageErr *render.UnsupportedIndexStorageParametersError
	require.ErrorAs(t, err, &storageErr)
	require.Equal(t, "documents_status_idx", storageErr.Index)
	require.Equal(t, map[string]string{"fillfactor": "70"}, storageErr.StorageParameters)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexStorageParameters)
}

// TestCreateIndexesRejectsTablespace proves that an IndexDef naming a
// Tablespace, which inspect now describes instead of rejecting, is refused
// at render time with a typed error rather than silently rendered into the
// database's default tablespace.
func TestCreateIndexesRejectsTablespace(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:       "documents_status_idx",
			Columns:    []string{"status"},
			Tablespace: "pg_custom",
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, `"pg_custom"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var tablespaceErr *render.UnsupportedIndexTablespaceError
	require.ErrorAs(t, err, &tablespaceErr)
	require.Equal(t, "documents_status_idx", tablespaceErr.Index)
	require.Equal(t, "pg_custom", tablespaceErr.Tablespace)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexTablespace)
}

// TestCreateIndexesRejectsReplicaIdentity proves that an IndexDef setting
// ReplicaIdentity, which inspect now describes instead of rejecting, is
// refused at render time with a typed error rather than silently rendered
// as a plain index with no bearing on logical replication.
func TestCreateIndexesRejectsReplicaIdentity(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:            "documents_status_idx",
			Columns:         []string{"status"},
			Unique:          true,
			ReplicaIdentity: true,
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, "replica identity")
	require.ErrorContains(t, err, "can describe but not yet render")

	var replicaIdentityErr *render.UnsupportedIndexReplicaIdentityError
	require.ErrorAs(t, err, &replicaIdentityErr)
	require.Equal(t, "documents_status_idx", replicaIdentityErr.Index)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexReplicaIdentity)
}

// TestCreateTableRejectsNonDefaultForeignKeyMatch proves that a
// ForeignKeyDef naming a non-default schema.MatchType, such as MATCH FULL
// which inspect now describes instead of rejecting, is refused at render
// time with a typed error rather than silently rendered as a plain MATCH
// SIMPLE foreign key: rasql does not yet know how to build DDL for
// anything other than a plain MATCH SIMPLE foreign key, and a foreign key
// renders inline in CREATE TABLE, unlike an index, so the table itself is
// refused too.
func TestCreateTableRejectsNonDefaultForeignKeyMatch(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			Match:             schema.MatchFull,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "MATCH FULL")
	require.ErrorContains(t, err, "can describe but not yet render")

	var matchErr *render.UnsupportedForeignKeyMatchError
	require.ErrorAs(t, err, &matchErr)
	require.Equal(t, "orders_customer_fk", matchErr.ForeignKey)
	require.Equal(t, schema.MatchFull, matchErr.Match)
	require.ErrorIs(t, err, render.ErrUnsupportedForeignKeyMatch)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)
}

// TestCreateTableRejectsNonDefaultForeignKeyDeferrability proves that a
// ForeignKeyDef naming a non-default schema.Deferrability, such as
// DEFERRABLE INITIALLY DEFERRED which inspect now describes instead of
// rejecting, is refused at render time with a typed error rather than
// silently rendered as a plain NOT DEFERRABLE foreign key.
func TestCreateTableRejectsNonDefaultForeignKeyDeferrability(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			Deferrable:        schema.DeferrableInitiallyDeferred,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var deferrableErr *render.UnsupportedForeignKeyDeferrabilityError
	require.ErrorAs(t, err, &deferrableErr)
	require.Equal(t, "orders_customer_fk", deferrableErr.ForeignKey)
	require.Equal(t, schema.DeferrableInitiallyDeferred, deferrableErr.Deferrable)
	require.ErrorIs(t, err, render.ErrUnsupportedForeignKeyDeferrability)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)
}

// TestCreateTableRejectsExclusionConstraint proves that a TableDef naming an
// ExclusionDef, which inspect now describes instead of rejecting, is
// refused at render time with a typed error rather than silently rendered
// as a table missing the constraint entirely: an EXCLUDE clause renders
// inline in CREATE TABLE, like a foreign key, so the table itself is
// refused too.
func TestCreateTableRejectsExclusionConstraint(t *testing.T) {
	table := schema.TableDef{
		Name: "reservations",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "room", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name:     "reservations_no_double_booking",
			Method:   "gist",
			Elements: []schema.ExclusionElementDef{{Expression: "room", Operator: "="}},
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"reservations_no_double_booking"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var exclusionErr *render.UnsupportedExclusionConstraintError
	require.ErrorAs(t, err, &exclusionErr)
	require.Equal(t, "reservations_no_double_booking", exclusionErr.Exclusion)
	require.ErrorIs(t, err, render.ErrUnsupportedExclusionConstraint)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)
}

// TestCreateTableRejectsCheckNoInherit proves that a CheckDef naming
// NoInherit, such as a NO INHERIT check constraint inspect now describes
// instead of rejecting, is refused at render time with a typed error rather
// than silently rendered as a plain inherited check constraint.
func TestCreateTableRejectsCheckNoInherit(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{{
			Name:       "invoices_amount_check",
			Expression: "amount >= 0",
			NoInherit:  true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"invoices_amount_check"`)
	require.ErrorContains(t, err, "NO INHERIT")
	require.ErrorContains(t, err, "can describe but not yet render")

	var noInheritErr *render.UnsupportedCheckNoInheritError
	require.ErrorAs(t, err, &noInheritErr)
	require.Equal(t, "invoices_amount_check", noInheritErr.Check)
	require.ErrorIs(t, err, render.ErrUnsupportedCheckNoInherit)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)
}

// TestCreateTableRejectsCheckNotValid proves that a CheckDef naming
// NotValid, such as a NOT VALID check constraint inspect now describes
// instead of rejecting, is refused at render time with a typed error rather
// than silently rendered as a plain validated check constraint.
func TestCreateTableRejectsCheckNotValid(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{{
			Name:       "invoices_amount_check",
			Expression: "amount >= 0",
			NotValid:   true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"invoices_amount_check"`)
	require.ErrorContains(t, err, "NOT VALID")
	require.ErrorContains(t, err, "can describe but not yet render")

	var notValidErr *render.UnsupportedCheckNotValidError
	require.ErrorAs(t, err, &notValidErr)
	require.Equal(t, "invoices_amount_check", notValidErr.Check)
	require.ErrorIs(t, err, render.ErrUnsupportedCheckNotValid)
}

// TestCreateTableRejectsCheckNotEnforced proves that a CheckDef naming
// NotEnforced, such as a NOT ENFORCED check constraint inspect now
// describes instead of rejecting, is refused at render time with a typed
// error rather than silently rendered as a plain enforced check constraint.
func TestCreateTableRejectsCheckNotEnforced(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{{
			Name:        "invoices_amount_check",
			Expression:  "amount >= 0",
			NotEnforced: true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"invoices_amount_check"`)
	require.ErrorContains(t, err, "NOT ENFORCED")
	require.ErrorContains(t, err, "can describe but not yet render")

	var notEnforcedErr *render.UnsupportedCheckNotEnforcedError
	require.ErrorAs(t, err, &notEnforcedErr)
	require.Equal(t, "invoices_amount_check", notEnforcedErr.Check)
	require.ErrorIs(t, err, render.ErrUnsupportedCheckNotEnforced)
}

// TestCreateTableRejectsForeignKeyNotValid proves that a ForeignKeyDef
// naming NotValid, such as a NOT VALID foreign key inspect now describes
// instead of rejecting, is refused at render time with a typed error rather
// than silently rendered as a plain validated foreign key.
func TestCreateTableRejectsForeignKeyNotValid(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			NotValid:          true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "NOT VALID")
	require.ErrorContains(t, err, "can describe but not yet render")

	var notValidErr *render.UnsupportedForeignKeyNotValidError
	require.ErrorAs(t, err, &notValidErr)
	require.Equal(t, "orders_customer_fk", notValidErr.ForeignKey)
	require.ErrorIs(t, err, render.ErrUnsupportedForeignKeyNotValid)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)
}

// TestCreateTableRejectsForeignKeyNotEnforced proves that a ForeignKeyDef
// naming NotEnforced, such as a NOT ENFORCED foreign key inspect now
// describes instead of rejecting, is refused at render time with a typed
// error rather than silently rendered as a plain enforced foreign key.
func TestCreateTableRejectsForeignKeyNotEnforced(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			NotEnforced:       true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"orders_customer_fk"`)
	require.ErrorContains(t, err, "NOT ENFORCED")
	require.ErrorContains(t, err, "can describe but not yet render")

	var notEnforcedErr *render.UnsupportedForeignKeyNotEnforcedError
	require.ErrorAs(t, err, &notEnforcedErr)
	require.Equal(t, "orders_customer_fk", notEnforcedErr.ForeignKey)
	require.ErrorIs(t, err, render.ErrUnsupportedForeignKeyNotEnforced)
}

// TestCreateTableRejectsNonDefaultUniqueDeferrability proves that a
// UniqueDef naming a non-default schema.Deferrability, such as DEFERRABLE
// INITIALLY DEFERRED which inspect now describes instead of rejecting, is
// refused at render time with a typed error rather than silently rendered
// as a plain NOT DEFERRABLE unique constraint.
func TestCreateTableRejectsNonDefaultUniqueDeferrability(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:       "accounts_email_key",
			Columns:    []string{"email"},
			Deferrable: schema.DeferrableInitiallyDeferred,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var deferrableErr *render.UnsupportedUniqueDeferrabilityError
	require.ErrorAs(t, err, &deferrableErr)
	require.Equal(t, "accounts_email_key", deferrableErr.Unique)
	require.Equal(t, schema.DeferrableInitiallyDeferred, deferrableErr.Deferrable)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueDeferrability)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "postgresql", renderErr.Dialect)
}

// TestCreateTableRejectsUniqueNullsNotDistinct proves that a UniqueDef
// setting NullsNotDistinct, such as a live PostgreSQL 15+ UNIQUE NULLS NOT
// DISTINCT constraint inspect now describes instead of rejecting, is
// refused at render time with a typed error rather than silently rendered
// as a plain NULLS DISTINCT constraint, which would accept a second NULL
// the real constraint rejects.
func TestCreateTableRejectsUniqueNullsNotDistinct(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:             "accounts_email_key",
			Columns:          []string{"email"},
			NullsNotDistinct: true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, "NULLS NOT DISTINCT")
	require.ErrorContains(t, err, "can describe but not yet render")

	var nullsErr *render.UnsupportedUniqueNullsNotDistinctError
	require.ErrorAs(t, err, &nullsErr)
	require.Equal(t, "accounts_email_key", nullsErr.Unique)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueNullsNotDistinct)
}

// TestCreateTableRejectsUniqueIncludeColumns proves that a UniqueDef naming
// IncludeColumns, such as a live PostgreSQL INCLUDE clause inspect now
// describes instead of rejecting, is refused at render time with a typed
// error rather than silently rendered without the covering columns.
func TestCreateTableRejectsUniqueIncludeColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:           "accounts_email_key",
			Columns:        []string{"email"},
			IncludeColumns: []string{"name"},
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, `["name"]`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var includeErr *render.UnsupportedUniqueIncludeColumnsError
	require.ErrorAs(t, err, &includeErr)
	require.Equal(t, "accounts_email_key", includeErr.Unique)
	require.Equal(t, []string{"name"}, includeErr.IncludeColumns)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueIncludeColumns)
}

// TestCreateTableRejectsUniqueConflictResolution proves that a UniqueDef
// naming a non-default schema.ConflictResolution, such as a live SQLite ON
// CONFLICT REPLACE clause inspect now describes instead of rejecting, is
// refused at render time with a typed error rather than silently rendered
// as a plain UNIQUE constraint with no conflict resolution.
func TestCreateTableRejectsUniqueConflictResolution(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:       "accounts_email_key",
			Columns:    []string{"email"},
			OnConflict: schema.ConflictReplace,
		}},
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, "ON CONFLICT REPLACE")
	require.ErrorContains(t, err, "can describe but not yet render")

	var conflictErr *render.UnsupportedUniqueConflictResolutionError
	require.ErrorAs(t, err, &conflictErr)
	require.Equal(t, "accounts_email_key", conflictErr.Unique)
	require.Equal(t, schema.ConflictReplace, conflictErr.OnConflict)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueConflictResolution)
}

// TestCreateTableRejectsStrictTable proves that a TableDef setting Strict,
// such as a live SQLite STRICT table inspect now describes instead of
// rejecting, is refused at render time with a typed error rather than
// silently rendered as a plain, non-STRICT table.
func TestCreateTableRejectsStrictTable(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Strict:     true,
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"users"`)
	require.ErrorContains(t, err, "STRICT")
	require.ErrorContains(t, err, "can describe but not yet render")

	var strictErr *render.UnsupportedTableStrictError
	require.ErrorAs(t, err, &strictErr)
	require.Equal(t, "users", strictErr.Table)
	require.ErrorIs(t, err, render.ErrUnsupportedTableStrict)

	var renderErr *render.Error
	require.ErrorAs(t, err, &renderErr)
	require.Equal(t, "sqlite", renderErr.Dialect)
}

// TestCreateTableRejectsWithoutRowIDTable is the WithoutRowID counterpart
// to TestCreateTableRejectsStrictTable.
func TestCreateTableRejectsWithoutRowIDTable(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey:   []string{"id"},
		WithoutRowID: true,
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"users"`)
	require.ErrorContains(t, err, "WITHOUT ROWID")
	require.ErrorContains(t, err, "can describe but not yet render")

	var withoutRowIDErr *render.UnsupportedTableWithoutRowIDError
	require.ErrorAs(t, err, &withoutRowIDErr)
	require.Equal(t, "users", withoutRowIDErr.Table)
	require.ErrorIs(t, err, render.ErrUnsupportedTableWithoutRowID)
}

// TestCreateTableRejectsPrimaryKeyAutoincrement proves that a TableDef
// setting PrimaryKeyAutoincrement, such as a live SQLite AUTOINCREMENT
// primary key inspect now describes instead of rejecting, is refused at
// render time with a typed error rather than silently rendered as a plain
// primary key with no AUTOINCREMENT keyword.
func TestCreateTableRejectsPrimaryKeyAutoincrement(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey:              []string{"id"},
		PrimaryKeyAutoincrement: true,
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"users"`)
	require.ErrorContains(t, err, "AUTOINCREMENT")
	require.ErrorContains(t, err, "can describe but not yet render")

	var autoincrementErr *render.UnsupportedPrimaryKeyAutoincrementError
	require.ErrorAs(t, err, &autoincrementErr)
	require.Equal(t, "users", autoincrementErr.Table)
	require.ErrorIs(t, err, render.ErrUnsupportedPrimaryKeyAutoincrement)
}

// TestCreateTableRejectsPrimaryKeyConflictResolution proves that a
// TableDef naming a non-default schema.ConflictResolution on its primary
// key, such as a live SQLite primary key's ON CONFLICT REPLACE clause
// inspect now describes instead of rejecting, is refused at render time
// with a typed error rather than silently rendered as a plain primary key
// with no conflict resolution.
func TestCreateTableRejectsPrimaryKeyConflictResolution(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey:           []string{"id"},
		PrimaryKeyOnConflict: schema.ConflictReplace,
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"users"`)
	require.ErrorContains(t, err, "ON CONFLICT REPLACE")
	require.ErrorContains(t, err, "can describe but not yet render")

	var conflictErr *render.UnsupportedPrimaryKeyConflictResolutionError
	require.ErrorAs(t, err, &conflictErr)
	require.Equal(t, "users", conflictErr.Table)
	require.Equal(t, schema.ConflictReplace, conflictErr.OnConflict)
	require.ErrorIs(t, err, render.ErrUnsupportedPrimaryKeyConflictResolution)
}

// TestCreateTableRejectsVirtualTable proves that a TableDef naming
// VirtualTableModule, such as a live SQLite FTS5 virtual table inspect now
// describes instead of rejecting, is refused at render time with a typed
// error rather than silently rendered as an ordinary table.
func TestCreateTableRejectsVirtualTable(t *testing.T) {
	table := schema.TableDef{
		Name: "posts_fts",
		Columns: []schema.ColumnDef{
			{Name: "body", Type: schema.TextType{}, Nullable: true},
		},
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"body"},
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"posts_fts"`)
	require.ErrorContains(t, err, `"fts5"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var virtualErr *render.UnsupportedVirtualTableError
	require.ErrorAs(t, err, &virtualErr)
	require.Equal(t, "posts_fts", virtualErr.Table)
	require.Equal(t, "fts5", virtualErr.Module)
	require.ErrorIs(t, err, render.ErrUnsupportedVirtualTable)
}

// TestCreateTableRejectsUniqueKeyDetails proves that a UniqueDef naming
// Keys, such as a live SQLite UNIQUE constraint with a DESC or
// non-default-collation key inspect now describes instead of rejecting, is
// refused at render time with a typed error rather than silently rendered
// as a plain ascending, default-collation constraint.
func TestCreateTableRejectsUniqueKeyDetails(t *testing.T) {
	table := schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{Name: "members_email_key", Keys: []schema.IndexKeyDef{{Expression: "email", Descending: true}}},
		},
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"members_email_key"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var keysErr *render.UnsupportedUniqueKeyDetailsError
	require.ErrorAs(t, err, &keysErr)
	require.Equal(t, "members_email_key", keysErr.Unique)
	require.Equal(t, table.UniqueConstraints[0].Keys, keysErr.Keys)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueKeyDetails)
}

// TestCreateTableRejectsGeneratedColumn proves that a ColumnDef naming a
// GeneratedExpression, such as a live SQLite generated column inspect now
// describes instead of rejecting, is refused at render time with a typed
// error rather than silently rendered as a plain writable column: a
// generated column cannot be written to at all, so that silent substitution
// would be a worse failure than most this package refuses.
func TestCreateTableRejectsGeneratedColumn(t *testing.T) {
	table := schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.SQLite(), table)
	require.ErrorContains(t, err, `"fahrenheit"`)
	require.ErrorContains(t, err, "celsius * 9 / 5 + 32")
	require.ErrorContains(t, err, "can describe but not yet render")

	var generatedErr *render.UnsupportedGeneratedColumnError
	require.ErrorAs(t, err, &generatedErr)
	require.Equal(t, "fahrenheit", generatedErr.Column)
	require.Equal(t, "celsius * 9 / 5 + 32", generatedErr.Expression)
	require.Equal(t, schema.GeneratedStored, generatedErr.Storage)
	require.ErrorIs(t, err, render.ErrUnsupportedGeneratedColumn)
}

// TestCreateTableRejectsIntegerDisplayWidth proves that an IntegerType
// naming a stated DisplayWidth, such as the 11 in a live MySQL int(11)
// column inspect now describes instead of rejecting, is refused at render
// time with a typed error rather than silently rendered without it.
func TestCreateTableRejectsIntegerDisplayWidth(t *testing.T) {
	table := schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(11)}},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `"total"`)
	require.ErrorContains(t, err, "11")
	require.ErrorContains(t, err, "can describe but not yet render")

	var widthErr *render.UnsupportedIntegerDisplayWidthError
	require.ErrorAs(t, err, &widthErr)
	require.Equal(t, "total", widthErr.Column)
	require.Equal(t, 11, widthErr.Width)
	require.ErrorIs(t, err, render.ErrUnsupportedIntegerDisplayWidth)
}

// TestCreateTableRejectsIntegerZeroFill is the ZEROFILL counterpart to
// TestCreateTableRejectsIntegerDisplayWidth.
func TestCreateTableRejectsIntegerZeroFill(t *testing.T) {
	table := schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{Unsigned: true, ZeroFill: true}},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `"total"`)
	require.ErrorContains(t, err, "ZEROFILL")
	require.ErrorContains(t, err, "can describe but not yet render")

	var zeroFillErr *render.UnsupportedIntegerZeroFillError
	require.ErrorAs(t, err, &zeroFillErr)
	require.Equal(t, "total", zeroFillErr.Column)
	require.ErrorIs(t, err, render.ErrUnsupportedIntegerZeroFill)
}

// TestCreateTableRejectsDecimalUnsigned proves that a DecimalType naming a
// true Unsigned, such as what inspect now describes for a live MySQL
// DECIMAL(p,s) UNSIGNED column, is refused at render time with a typed error
// rather than silently rendered without it.
func TestCreateTableRejectsDecimalUnsigned(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true}},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `"amount"`)
	require.ErrorContains(t, err, "UNSIGNED")
	require.ErrorContains(t, err, "can describe but not yet render")

	var unsignedErr *render.UnsupportedDecimalUnsignedError
	require.ErrorAs(t, err, &unsignedErr)
	require.Equal(t, "amount", unsignedErr.Column)
	require.ErrorIs(t, err, render.ErrUnsupportedDecimalUnsigned)
}

// TestCreateTableRejectsDecimalZeroFill is the ZEROFILL counterpart to
// TestCreateTableRejectsDecimalUnsigned.
func TestCreateTableRejectsDecimalZeroFill(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true}},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `"amount"`)
	require.ErrorContains(t, err, "ZEROFILL")
	require.ErrorContains(t, err, "can describe but not yet render")

	var zeroFillErr *render.UnsupportedDecimalZeroFillError
	require.ErrorAs(t, err, &zeroFillErr)
	require.Equal(t, "amount", zeroFillErr.Column)
	require.ErrorIs(t, err, render.ErrUnsupportedDecimalZeroFill)
}

func TestCreateTableReportsDecimalTypeErrorWithColumn(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 100, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `column "amount"`)
	require.ErrorContains(t, err, "decimal precision 100 exceeds the maximum of 65")
}

// TestCreateTableRendersQualifiedName pins CREATE TABLE for a schema-qualified
// table across all three built-in dialects. No dialect.Capability is
// consulted for CREATE TABLE itself: quoteQualified renders two identifiers
// joined by a dot on every dialect, and section 3.2 of the design measured
// that all three accept that form.
func TestCreateTableRendersQualifiedName(t *testing.T) {
	table := schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql:     `CREATE TABLE "audit"."events" ("id" BIGINT NOT NULL, PRIMARY KEY ("id"))`,
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql:     "CREATE TABLE `audit`.`events` (`id` BIGINT NOT NULL, PRIMARY KEY (`id`))",
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql:     `CREATE TABLE "audit"."events" ("id" INTEGER NOT NULL, PRIMARY KEY ("id"))`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
		})
	}
}

// TestCreateTableRendersQualifiedForeignKey covers ForeignKey.ReferencedSchema.
// PostgreSQL and MySQL both hold dialect.CapabilityQualifiedReference and
// render a cross-schema REFERENCES clause qualified. SQLite holds neither: a
// same-schema reference still renders unqualified, because dropping a
// same-schema qualifier changes nothing about what the reference means and
// is the only form SQLite's grammar accepts at all (section 3.2 of the
// design), while a genuinely cross-schema reference is refused rather than
// silently narrowed to the wrong table.
func TestCreateTableRendersQualifiedForeignKey(t *testing.T) {
	crossSchema := schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "events_user_id_fkey",
			Columns:           []string{"user_id"},
			ReferencedSchema:  "tenant",
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
	}

	rendered, err := render.CreateTable(dialect.PostgreSQL(), crossSchema)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), `REFERENCES "tenant"."users" ("id") ON DELETE CASCADE`)

	rendered, err = render.CreateTable(dialect.MySQL(), crossSchema)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), "REFERENCES `tenant`.`users` (`id`) ON DELETE CASCADE")

	_, err = render.CreateTable(dialect.SQLite(), crossSchema)
	require.ErrorContains(t, err, "dialect.CapabilityQualifiedReference")
	require.ErrorContains(t, err, `"tenant"`)
	require.ErrorContains(t, err, `"audit"`)

	sameSchema := crossSchema
	sameSchema.ForeignKeys = []schema.ForeignKeyDef{{
		Name:              "events_user_id_fkey",
		Columns:           []string{"user_id"},
		ReferencedSchema:  "audit",
		ReferencedTable:   "users",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.Cascade,
	}}
	rendered, err = render.CreateTable(dialect.SQLite(), sameSchema)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), `REFERENCES "users" ("id") ON DELETE CASCADE`)
}

// TestCreateIndexRendersDialectQualifierPosition covers writeCreateIndex's
// two mutually exclusive capabilities. PostgreSQL and MySQL hold
// dialect.CapabilityQualifiedIndexTarget and qualify the indexed table,
// leaving the index name bare. SQLite holds dialect.CapabilityQualifiedIndexName
// instead and qualifies the index name, leaving the indexed table bare,
// because it cannot qualify the table in "ON table" at all (section 3.2 of
// the design). Both the plain and the UNIQUE form are covered, since the
// qualifier position is decided before the UNIQUE keyword is even written.
func TestCreateIndexRendersDialectQualifierPosition(t *testing.T) {
	table := schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{
			{Name: "events_user_id_idx", Columns: []string{"user_id"}},
			{Name: "events_user_id_uidx", Columns: []string{"user_id"}, Unique: true},
		},
	}

	tests := map[string]struct {
		dialect dialect.Dialect
		sql     []string
	}{
		"postgresql": {
			dialect: dialect.PostgreSQL(),
			sql: []string{
				`CREATE INDEX "events_user_id_idx" ON "audit"."events" ("user_id")`,
				`CREATE UNIQUE INDEX "events_user_id_uidx" ON "audit"."events" ("user_id")`,
			},
		},
		"mysql": {
			dialect: dialect.MySQL(),
			sql: []string{
				"CREATE INDEX `events_user_id_idx` ON `audit`.`events` (`user_id`)",
				"CREATE UNIQUE INDEX `events_user_id_uidx` ON `audit`.`events` (`user_id`)",
			},
		},
		"sqlite": {
			dialect: dialect.SQLite(),
			sql: []string{
				`CREATE INDEX "audit"."events_user_id_idx" ON "events" ("user_id")`,
				`CREATE UNIQUE INDEX "audit"."events_user_id_uidx" ON "events" ("user_id")`,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			statements, err := render.CreateIndexes(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.sql, sqls(statements))
		})
	}
}

// noQualifiedDDLDialect wraps a dialect.Dialect and reports false for every
// capability this change adds, regardless of what the wrapped dialect
// actually supports. It stands in for any third-party dialect.Dialect
// implementation written before these three constants existed: Capability is
// a growing bitmask, and an implementation outside this module returns false
// for a bit it has never heard of rather than panicking or guessing.
type noQualifiedDDLDialect struct {
	dialect.Dialect
}

func (d noQualifiedDDLDialect) Supports(capability dialect.Capability) bool {
	switch capability {
	case dialect.CapabilityQualifiedReference, dialect.CapabilityQualifiedIndexTarget, dialect.CapabilityQualifiedIndexName:
		return false
	default:
		return d.Dialect.Supports(capability)
	}
}

// TestUnqualifiedDDLIsUnchanged is the regression guard for every existing
// user of render.CreateTable and render.CreateIndexes: a descriptor that
// names no schema anywhere renders exactly what it rendered before this
// change, on all three built-in dialects and on a third-party dialect that
// has never heard of the three capabilities this change adds. That holds
// because the empty-schema paths in writeCreateTable, qualifiedIndexNames and
// qualifiedReferencedTable never call dialect.Dialect.Supports at all.
func TestUnqualifiedDDLIsUnchanged(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "metadata", Type: schema.JSONType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:    "orders_customer_key",
			Columns: []string{"customer_id"},
		}},
		Checks: []schema.CheckDef{{
			Name:       "orders_id_check",
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
		Indexes: []schema.IndexDef{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
	}

	tests := map[string]struct {
		dialect     dialect.Dialect
		createTable string
		createIndex string
	}{
		"postgresql": {
			dialect:     dialect.PostgreSQL(),
			createTable: `CREATE TABLE "orders" ("id" BIGINT NOT NULL, "customer_id" BIGINT NOT NULL, "metadata" JSONB, PRIMARY KEY ("id"), CONSTRAINT "orders_customer_key" UNIQUE ("customer_id"), CONSTRAINT "orders_id_check" CHECK (id > 0), CONSTRAINT "orders_customer_fk" FOREIGN KEY ("customer_id") REFERENCES "customers" ("id") ON DELETE CASCADE)`,
			createIndex: `CREATE INDEX "orders_customer_idx" ON "orders" ("customer_id")`,
		},
		"mysql": {
			dialect:     dialect.MySQL(),
			createTable: "CREATE TABLE `orders` (`id` BIGINT NOT NULL, `customer_id` BIGINT NOT NULL, `metadata` JSON, PRIMARY KEY (`id`), CONSTRAINT `orders_customer_key` UNIQUE (`customer_id`), CONSTRAINT `orders_id_check` CHECK (id > 0), CONSTRAINT `orders_customer_fk` FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`) ON DELETE CASCADE)",
			createIndex: "CREATE INDEX `orders_customer_idx` ON `orders` (`customer_id`)",
		},
		"sqlite": {
			dialect:     dialect.SQLite(),
			createTable: `CREATE TABLE "orders" ("id" INTEGER NOT NULL, "customer_id" INTEGER NOT NULL, "metadata" TEXT, PRIMARY KEY ("id"), CONSTRAINT "orders_customer_key" UNIQUE ("customer_id"), CONSTRAINT "orders_id_check" CHECK (id > 0), CONSTRAINT "orders_customer_fk" FOREIGN KEY ("customer_id") REFERENCES "customers" ("id") ON DELETE CASCADE)`,
			createIndex: `CREATE INDEX "orders_customer_idx" ON "orders" ("customer_id")`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rendered, err := render.CreateTable(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, test.createTable, rendered.SQL())

			indexes, err := render.CreateIndexes(test.dialect, table)
			require.NoError(t, err)
			require.Equal(t, []string{test.createIndex}, sqls(indexes))

			stub := noQualifiedDDLDialect{Dialect: test.dialect}
			rendered, err = render.CreateTable(stub, table)
			require.NoError(t, err)
			require.Equal(t, test.createTable, rendered.SQL())

			indexes, err = render.CreateIndexes(stub, table)
			require.NoError(t, err)
			require.Equal(t, []string{test.createIndex}, sqls(indexes))
		})
	}
}

// TestCreateTableRendersQualifiedDecimalColumn pins that qualification and
// the decimal-column work (schema.DecimalType's Precision/Scale rendering)
// do not interact: a decimal column inside a qualified table renders its
// NUMERIC(p,s) type exactly as it would unqualified, next to a qualified
// table name.
func TestCreateTableRendersQualifiedDecimalColumn(t *testing.T) {
	table := schema.TableDef{
		Schema: "audit",
		Name:   "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
	}

	rendered, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
	require.Equal(t, `CREATE TABLE "audit"."invoices" ("id" BIGINT NOT NULL, "amount" NUMERIC(19,4) NOT NULL, PRIMARY KEY ("id"))`, rendered.SQL())
}

// TestSQLiteExecutesQualifiedDDL proves the SQLite branch against a real
// parser rather than a golden string: the rendered CREATE TABLE, CREATE
// INDEX and CREATE UNIQUE INDEX all execute, and every object lands in the
// attached "audit" database rather than "main".
func TestSQLiteExecutesQualifiedDDL(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(t.Context(), `ATTACH DATABASE ':memory:' AS audit`)
	require.NoError(t, err)

	table := schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{
			{Name: "events_user_id_idx", Columns: []string{"user_id"}},
			{Name: "events_user_id_uidx", Columns: []string{"user_id"}, Unique: true},
		},
	}

	created, err := render.CreateTable(dialect.SQLite(), table)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), created.SQL())
	require.NoError(t, err)

	indexes, err := render.CreateIndexes(dialect.SQLite(), table)
	require.NoError(t, err)
	for _, index := range indexes {
		_, err = database.ExecContext(t.Context(), index.SQL())
		require.NoError(t, err)
	}

	rows, err := database.QueryContext(t.Context(), `SELECT name FROM audit.sqlite_schema WHERE type IN ('table', 'index') ORDER BY name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"events", "events_user_id_idx", "events_user_id_uidx"}, names)

	var mainCount int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM main.sqlite_schema WHERE type IN ('table', 'index')`).Scan(&mainCount))
	require.Zero(t, mainCount)
}

// TestCreateIndexesRejectsNullsNotDistinct proves that an IndexDef setting
// NullsNotDistinct, such as a live PostgreSQL plain unique index declared
// NULLS NOT DISTINCT inspect now describes instead of rejecting outright, is
// refused at render time with a typed error rather than silently rendered
// as an ordinary NULLS DISTINCT index.
func TestCreateIndexesRejectsNullsNotDistinct(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:             "documents_status_idx",
			Columns:          []string{"status"},
			Unique:           true,
			NullsNotDistinct: true,
		}},
	}

	_, err := render.CreateIndexes(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"documents_status_idx"`)
	require.ErrorContains(t, err, "NULLS NOT DISTINCT")
	require.ErrorContains(t, err, "can describe but not yet render")

	var nullsErr *render.UnsupportedIndexNullsNotDistinctError
	require.ErrorAs(t, err, &nullsErr)
	require.Equal(t, "documents_status_idx", nullsErr.Index)
	require.ErrorIs(t, err, render.ErrUnsupportedIndexNullsNotDistinct)
}

// TestCreateTableRejectsUniqueTemporal proves that a UniqueDef setting
// Temporal, such as a live PostgreSQL 18 WITHOUT OVERLAPS constraint
// inspect now describes instead of rejecting outright, is refused at
// render time with a typed error rather than silently rendered as an
// ordinary, non-temporal UNIQUE constraint.
func TestCreateTableRejectsUniqueTemporal(t *testing.T) {
	table := schema.TableDef{
		Name: "reservations",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "room", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:     "reservations_room_key",
			Columns:  []string{"room"},
			Temporal: true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"reservations_room_key"`)
	require.ErrorContains(t, err, "temporal")
	require.ErrorContains(t, err, "can describe but not yet render")

	var temporalErr *render.UnsupportedUniqueTemporalError
	require.ErrorAs(t, err, &temporalErr)
	require.Equal(t, "reservations_room_key", temporalErr.Unique)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueTemporal)
}

// TestCreateTableRejectsUniqueStorageParameters proves that a UniqueDef
// naming StorageParameters, such as a live PostgreSQL fillfactor on a
// unique constraint's backing index inspect now describes instead of
// rejecting outright, is refused at render time with a typed error rather
// than silently rendered without its WITH (...) clause.
func TestCreateTableRejectsUniqueStorageParameters(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:              "accounts_email_key",
			Columns:           []string{"email"},
			StorageParameters: map[string]string{"fillfactor": "70"},
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, "storage parameters")
	require.ErrorContains(t, err, "can describe but not yet render")

	var storageErr *render.UnsupportedUniqueStorageParametersError
	require.ErrorAs(t, err, &storageErr)
	require.Equal(t, "accounts_email_key", storageErr.Unique)
	require.Equal(t, map[string]string{"fillfactor": "70"}, storageErr.StorageParameters)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueStorageParameters)
}

// TestCreateTableRejectsUniqueTablespace proves that a UniqueDef naming a
// Tablespace, which inspect now describes instead of rejecting outright,
// is refused at render time with a typed error rather than silently
// rendered into the database's default tablespace.
func TestCreateTableRejectsUniqueTablespace(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:       "accounts_email_key",
			Columns:    []string{"email"},
			Tablespace: "pg_custom",
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, `"pg_custom"`)
	require.ErrorContains(t, err, "can describe but not yet render")

	var tablespaceErr *render.UnsupportedUniqueTablespaceError
	require.ErrorAs(t, err, &tablespaceErr)
	require.Equal(t, "accounts_email_key", tablespaceErr.Unique)
	require.Equal(t, "pg_custom", tablespaceErr.Tablespace)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueTablespace)
}

// TestCreateTableRejectsUniqueReplicaIdentity proves that a UniqueDef
// setting ReplicaIdentity, which inspect now describes instead of
// rejecting outright, is refused at render time with a typed error rather
// than silently rendered as a plain constraint with no bearing on logical
// replication.
func TestCreateTableRejectsUniqueReplicaIdentity(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:            "accounts_email_key",
			Columns:         []string{"email"},
			ReplicaIdentity: true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, "replica identity")
	require.ErrorContains(t, err, "can describe but not yet render")

	var replicaIdentityErr *render.UnsupportedUniqueReplicaIdentityError
	require.ErrorAs(t, err, &replicaIdentityErr)
	require.Equal(t, "accounts_email_key", replicaIdentityErr.Unique)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueReplicaIdentity)
}

// TestCreateTableRejectsUniqueCollations proves that a UniqueDef naming
// Collations, which inspect now describes instead of rejecting outright,
// is refused at render time with a typed error rather than silently
// rendered with each column's own default collation.
func TestCreateTableRejectsUniqueCollations(t *testing.T) {
	table := schema.TableDef{
		Name: "accounts",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:       "accounts_email_key",
			Columns:    []string{"email"},
			Collations: map[string]string{"email": "C"},
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"accounts_email_key"`)
	require.ErrorContains(t, err, "column collations")
	require.ErrorContains(t, err, "can describe but not yet render")

	var collationsErr *render.UnsupportedUniqueCollationsError
	require.ErrorAs(t, err, &collationsErr)
	require.Equal(t, "accounts_email_key", collationsErr.Unique)
	require.Equal(t, map[string]string{"email": "C"}, collationsErr.Collations)
	require.ErrorIs(t, err, render.ErrUnsupportedUniqueCollations)
}

// TestCreateTableRejectsForeignKeyTemporal proves that a ForeignKeyDef
// setting Temporal, such as a live PostgreSQL 18 PERIOD foreign key
// inspect now describes instead of rejecting outright, is refused at
// render time with a typed error rather than silently rendered as an
// ordinary, non-temporal FOREIGN KEY.
func TestCreateTableRejectsForeignKeyTemporal(t *testing.T) {
	table := schema.TableDef{
		Name: "bookings",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "room_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "fk_bookings_room",
			Columns:           []string{"room_id"},
			ReferencedTable:   "rooms",
			ReferencedColumns: []string{"id"},
			Temporal:          true,
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"fk_bookings_room"`)
	require.ErrorContains(t, err, "temporal")
	require.ErrorContains(t, err, "can describe but not yet render")

	var temporalErr *render.UnsupportedForeignKeyTemporalError
	require.ErrorAs(t, err, &temporalErr)
	require.Equal(t, "fk_bookings_room", temporalErr.ForeignKey)
	require.ErrorIs(t, err, render.ErrUnsupportedForeignKeyTemporal)
}

// TestCreateTableRejectsForeignKeyDeleteSetColumns proves that a
// ForeignKeyDef naming DeleteSetColumns, such as a live PostgreSQL ON
// DELETE SET NULL (columns) clause inspect now describes instead of
// rejecting outright, is refused at render time with a typed error rather
// than silently rendered as a plain ON DELETE SET NULL applying to every
// referencing column.
func TestCreateTableRejectsForeignKeyDeleteSetColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "bookings",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "room_id", Type: schema.IntegerType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "fk_bookings_room",
			Columns:           []string{"room_id"},
			ReferencedTable:   "rooms",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.SetNull,
			DeleteSetColumns:  []string{"room_id"},
		}},
	}

	_, err := render.CreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, `"fk_bookings_room"`)
	require.ErrorContains(t, err, "[room_id]")
	require.ErrorContains(t, err, "can describe but not yet render")

	var deleteSetErr *render.UnsupportedForeignKeyDeleteSetColumnsError
	require.ErrorAs(t, err, &deleteSetErr)
	require.Equal(t, "fk_bookings_room", deleteSetErr.ForeignKey)
	require.Equal(t, []string{"room_id"}, deleteSetErr.DeleteSetColumns)
	require.ErrorIs(t, err, render.ErrUnsupportedForeignKeyDeleteSetColumns)
}

func sqls(statements []render.Statement) []string {
	sql := make([]string, len(statements))
	for i, statement := range statements {
		sql[i] = statement.SQL()
	}
	return sql
}
