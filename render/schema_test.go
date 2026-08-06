package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestCreateTableRendersDialectTypesAndConstraints(t *testing.T) {
	table := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "customer_id", Type: schema.TypeInteger},
			{Name: "metadata", Type: schema.TypeJSON, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueConstraint{{
			Name:    "orders_customer_key",
			Columns: []string{"customer_id"},
		}},
		Checks: []schema.CheckConstraint{{
			Name:       "orders_id_check",
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKey{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.ReferenceActionCascade,
		}},
		Indexes: []schema.Index{{
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
	table := schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: 4},
			{Name: "tax_rate", Type: schema.TypeDecimal, Precision: 5, Scale: 4, Nullable: true},
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

func TestCreateTableReportsDecimalTypeErrorWithColumn(t *testing.T) {
	table := schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 100, Scale: 4},
		},
		PrimaryKey: []string{"id"},
	}

	_, err := render.CreateTable(dialect.MySQL(), table)
	require.ErrorContains(t, err, `column "amount"`)
	require.ErrorContains(t, err, "decimal precision 100 exceeds the maximum of 65")
}

func sqls(statements []render.Statement) []string {
	sql := make([]string, len(statements))
	for i, statement := range statements {
		sql[i] = statement.SQL()
	}
	return sql
}
