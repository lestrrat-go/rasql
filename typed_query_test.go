package rasql

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type safeQueryRow struct{}

func TestSafeSelectBuilderUsesTypedPredicates(t *testing.T) {
	table, err := TableOf[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	statement, err := TypedSelectFrom(table).
		Where(query.EqualValue(query.TypedColumnOf[safeQueryRow, int64](table.Column("id")), int64(3))).
		Build(dialect.SQLite())
	require.NoError(t, err)
	require.Contains(t, statement.SQL(), "WHERE")
}

func TestTypedFacadeZeroValuesReturnErrors(t *testing.T) {
	var zeroTable Table[safeQueryRow]
	_, err := TypedSelectFrom(zeroTable).Select()
	require.Error(t, err)
	var zeroColumn query.TypedColumn[safeQueryRow, int64]
	_, err = TypedSelectFrom(TableFrom[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})).
		Where(query.EqualValue(zeroColumn, int64(1))).Select()
	require.Error(t, err)
	_, err = TypedSelectFrom(TableFrom[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})).
		Where(query.Predicate{}).Select()
	require.Error(t, err)
}
