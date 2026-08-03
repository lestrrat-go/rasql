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

func sqls(statements []render.Statement) []string {
	sql := make([]string, len(statements))
	for i, statement := range statements {
		sql[i] = statement.SQL()
	}
	return sql
}
