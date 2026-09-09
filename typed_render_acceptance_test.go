//go:build unix

package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// This proves Render, the typed counterpart of the removed
// TypedSelectBuilder.Build(dialect): a way to lower a typed Query to SQL text
// with no database involved at all. No engine is "reachable" or
// "unreachable" here on purpose — that is exactly the point of the
// capability, and every dialect it supports is exercised directly.

type renderGapRow struct {
	ID   int64
	Name string
}

type renderGapDecoder struct{ result ResultSchema }

func (d renderGapDecoder) ResultSchema() ResultSchema { return d.result }
func (d renderGapDecoder) Presence() []Presence       { return nil }
func (d renderGapDecoder) DecodeRow(src ScanSource, row *renderGapRow) error {
	return src.Scan(&row.ID, &row.Name)
}

func renderGapQuery(t *testing.T) Query[renderGapRow] {
	t.Helper()

	widgets, err := ReadTableOf[renderGapRow](schema.TableDef{Name: "widgets", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	w, err := SourceOf(widgets, "")
	require.NoError(t, err)

	id, err := BindColumn[renderGapRow, int64](w, "id", "")
	require.NoError(t, err)
	name, err := BindColumn[renderGapRow, string](w, "name", "")
	require.NoError(t, err)

	result, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		Item("id", id.Expr(), schema.IntegerType{}, ""),
		Item("name", name.Expr(), schema.TextType{}, ""),
	}, renderGapDecoder{result: result})
	require.NoError(t, err)

	return Select(w.Source(), projection).
		Where(GreaterValue(id.Expr(), int64(5))).
		OrderBy(AscExpr(name.Expr()))
}

// TestRenderProducesDialectSpecificSQL builds one query and renders it for
// all three dialects with no database at all. A Render that ignored d and
// always produced one dialect's quoting and placeholder style would still
// pass a test that checked only one dialect; asserting the exact SQL and
// args for all three here is what a wrong implementation cannot satisfy.
func TestRenderProducesDialectSpecificSQL(t *testing.T) {
	q := renderGapQuery(t)

	sqliteStatement, err := Render(q, dialect.SQLite())
	require.NoError(t, err)
	require.Equal(t, `SELECT "widgets"."id" AS "id", "widgets"."name" AS "name" FROM "widgets" WHERE ("widgets"."id" > ?) ORDER BY "widgets"."name"`, sqliteStatement.SQL())
	require.Equal(t, []any{int64(5)}, sqliteStatement.Args())

	postgresStatement, err := Render(q, dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, `SELECT "widgets"."id" AS "id", "widgets"."name" AS "name" FROM "widgets" WHERE ("widgets"."id" > $1) ORDER BY "widgets"."name"`, postgresStatement.SQL())
	require.Equal(t, []any{int64(5)}, postgresStatement.Args())

	mysqlStatement, err := Render(q, dialect.MySQL())
	require.NoError(t, err)
	require.Equal(t, "SELECT `widgets`.`id` AS `id`, `widgets`.`name` AS `name` FROM `widgets` WHERE (`widgets`.`id` > ?) ORDER BY `widgets`.`name`", mysqlStatement.SQL())
	require.Equal(t, []any{int64(5)}, mysqlStatement.Args())
}
