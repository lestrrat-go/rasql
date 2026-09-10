package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type pageAcceptanceRow struct{ ID int64 }
type pageAcceptanceDecoder struct{ schema ResultSchema }

func (d pageAcceptanceDecoder) ResultSchema() ResultSchema { return d.schema }
func (d pageAcceptanceDecoder) Presence() []Presence       { return nil }
func (d pageAcceptanceDecoder) DecodeRow(source ScanSource, row *pageAcceptanceRow) error {
	return source.Scan(&row.ID)
}

func TestPageAfterSQLiteTraversesRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE page_rows (id INTEGER NOT NULL)`)
	require.NoError(t, err)
	for i := 1; i <= 101; i++ {
		_, err = database.Exec(`INSERT INTO page_rows (id) VALUES (?)`, i)
		require.NoError(t, err)
	}
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := ReadTableOf[pageAcceptanceRow](schema.TableDef{Name: "page_rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "p")
	require.NoError(t, err)
	id, err := BindColumn[pageAcceptanceRow, int64](relation, "id", "")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, "")}, pageAcceptanceDecoder{schema: resultSchema})
	require.NoError(t, err)
	query := Select(relation.Source(), projection)
	key := AscKey[pageAcceptanceRow](id.Expr(), func(row pageAcceptanceRow) int64 { return row.ID })
	spec, err := NewPageSpec([]PageKey[pageAcceptanceRow]{key}, key)
	require.NoError(t, err)
	var all []int64
	request := PageRequest{Limit: 7}
	for {
		page, pageErr := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 7, MaxLimit: 100}, request)
		require.NoError(t, pageErr)
		for _, row := range page.Values {
			all = append(all, row.ID)
		}
		if !page.HasMore {
			break
		}
		request.After = page.Next
	}
	require.Len(t, all, 101)
	for i, value := range all {
		require.Equal(t, int64(i+1), value)
	}
}

func TestPageAfterRejectsBasePaging(t *testing.T) {
	var q Query[int]
	_, err := q.withKeysetOrder([]OrderTerm{{}})
	require.Error(t, err)
}
