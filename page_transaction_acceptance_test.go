package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestR5PageAfterExplicitTransactionUsesOneIsolationScope(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE transaction_page_rows (id INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO transaction_page_rows (id) VALUES (1),(2),(3),(4),(5)`)
	require.NoError(t, err)
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	txDB, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = txDB.Rollback() }()
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(txDB, profile)
	require.NoError(t, err)
	table, err := ReadTableOf[pageAcceptanceRow](schema.TableDef{Name: "transaction_page_rows", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
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
	request := PageRequest{Limit: 2}
	var values []int64
	for {
		page, pageErr := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 3}, request)
		require.NoError(t, pageErr)
		for _, row := range page.Values {
			values = append(values, row.ID)
		}
		if !page.HasMore {
			break
		}
		request.After = page.Next
	}
	require.Equal(t, []int64{1, 2, 3, 4, 5}, values)
	require.NoError(t, txDB.Commit())
}
