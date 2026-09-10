package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type sqliteRuntimeRow struct {
	ID      int64
	Name    Nullable[string]
	Payload Nullable[[]byte]
}
type sqliteRuntimeDecoder struct{ schema ResultSchema }

func (d sqliteRuntimeDecoder) ResultSchema() ResultSchema { return d.schema }
func (sqliteRuntimeDecoder) Presence() []Presence         { return nil }
func (sqliteRuntimeDecoder) DecodeRow(source ScanSource, result *sqliteRuntimeRow) error {
	return source.Scan(&result.ID, &result.Name, &result.Payload)
}

func TestSQLiteAsExecutorDirectScansRowsAndProjections(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`CREATE TABLE items (id INTEGER NOT NULL, name TEXT, payload BLOB)`)
	require.NoError(t, err)
	for i := 1; i <= 100; i++ {
		var name any = "name"
		var payload any = []byte{byte(i)}
		if i == 50 {
			name = nil
		}
		if i == 75 {
			payload = nil
		}
		_, err = database.Exec(`INSERT INTO items VALUES (?,?,?)`, i, name, payload)
		require.NoError(t, err)
	}
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := ReadTableOf[sqliteRuntimeRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}, Nullable: true}, {Name: "payload", Type: schema.BytesType{}, Nullable: true}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	id, err := BindColumn[sqliteRuntimeRow, int64](relation, "id", "")
	require.NoError(t, err)
	name, err := BindNullColumn[sqliteRuntimeRow, string](relation, "name", "")
	require.NoError(t, err)
	payload, err := BindNullColumn[sqliteRuntimeRow, []byte](relation, "payload", "")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "name", Type: schema.TextType{}, Nullable: true}, ResultColumn{Name: "payload", Type: schema.BytesType{}, Nullable: true})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, ""), NullItem("name", name.NullExpr(), schema.TextType{}, ""), NullItem("payload", payload.NullExpr(), schema.BytesType{}, "")}, sqliteRuntimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	query := Select(relation.Source(), projection)
	values, err := All(t.Context(), executor, query)
	require.NoError(t, err)
	require.Len(t, values, 100)
	require.Equal(t, int64(1), values[0].ID)
	require.Equal(t, byte(1), values[0].Payload.Value[0])
	require.False(t, values[49].Name.Valid)
	require.False(t, values[74].Payload.Valid)
	_, err = One(t.Context(), executor, query)
	require.ErrorIs(t, err, ErrMultipleRows)
	_, found, err := Maybe(t.Context(), executor, query)
	require.ErrorIs(t, err, ErrMultipleRows)
	require.False(t, found)
	empty, err := query.Limit(0)
	require.NoError(t, err)
	_, err = One(t.Context(), executor, empty)
	require.ErrorIs(t, err, ErrNoRows)
	_, found, err = Maybe(t.Context(), executor, empty)
	require.NoError(t, err)
	require.False(t, found)
}
