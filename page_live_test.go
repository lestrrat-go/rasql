//go:build unix

package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type r5LiveRow struct{ ID int64 }
type r5LiveDecoder struct{ schema ResultSchema }

func (d r5LiveDecoder) ResultSchema() ResultSchema { return d.schema }
func (r5LiveDecoder) Presence() []Presence         { return nil }
func (d r5LiveDecoder) DecodeRow(source ScanSource, row *r5LiveRow) error {
	return source.Scan(&row.ID)
}

func TestR5PageAfterLivePostgreSQL17AndMySQL84(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
		profile string
	}{
		{name: "postgresql-17", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL(), profile: "postgresql-17"},
		{name: "mysql-8.4", open: dbtest.MySQLDB, dialect: dialect.MySQL(), profile: "mysql-8.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := tc.open(t)
			tableName := dbtest.UniqueName(t, "rasql_r5_page")
			_, err := database.ExecContext(t.Context(), "CREATE TABLE "+tableName+" (id BIGINT NOT NULL)")
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = database.ExecContext(t.Context(), "DROP TABLE "+tableName) })
			_, err = database.ExecContext(t.Context(), "INSERT INTO "+tableName+" (id) VALUES (1),(2),(3),(4),(5)")
			require.NoError(t, err)
			db, err := New(database, tc.dialect)
			require.NoError(t, err)
			profile, err := EngineProfileFromVersion(tc.profile, map[string]int{"postgresql-17": 17, "mysql-8.4": 8}[tc.profile], map[string]int{"postgresql-17": 0, "mysql-8.4": 4}[tc.profile], 0)
			require.NoError(t, err)
			executor, err := AsExecutor(db, profile)
			require.NoError(t, err)
			table, err := ReadTableOf[r5LiveRow](schema.TableDef{Name: tableName, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
			require.NoError(t, err)
			relation, err := SourceOf(table, "p")
			require.NoError(t, err)
			id, err := BindColumn[r5LiveRow, int64](relation, "id", "")
			require.NoError(t, err)
			resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
			require.NoError(t, err)
			projection, err := NewProjection([]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, "")}, r5LiveDecoder{schema: resultSchema})
			require.NoError(t, err)
			query := Select(relation.Source(), projection)
			key := AscKey[r5LiveRow](id.Expr(), func(row r5LiveRow) int64 { return row.ID })
			spec, err := NewPageSpec([]PageKey[r5LiveRow]{key}, key)
			require.NoError(t, err)
			request := PageRequest{Limit: 2}
			var got []int64
			for {
				page, pageErr := PageAfter(t.Context(), executor, query, spec, PagePolicy{DefaultLimit: 2, MaxLimit: 10}, request)
				require.NoError(t, pageErr)
				for _, row := range page.Values {
					got = append(got, row.ID)
				}
				if !page.HasMore {
					break
				}
				request.After = page.Next
			}
			require.Equal(t, []int64{1, 2, 3, 4, 5}, got)
		})
	}
}
