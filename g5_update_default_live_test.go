//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type g5LiveRow struct{ ID, Value, Count int64 }

func TestG5LiveUpdateDefault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
		profile string
		create  string
	}{
		{"postgresql", dbtest.PostgreSQLDB, dialect.PostgreSQL(), "postgresql-17", "CREATE TABLE %s (id BIGINT PRIMARY KEY, value BIGINT NOT NULL DEFAULT 41, count BIGINT NOT NULL)"},
		{"mysql", dbtest.MySQLDB, dialect.MySQL(), "mysql-8.4", "CREATE TABLE %s (id BIGINT PRIMARY KEY, value BIGINT NOT NULL DEFAULT 41, count BIGINT NOT NULL)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := tc.open(t)
			name := dbtest.UniqueName(t, "rasql_g5_default")
			quoted, err := tc.dialect.QuoteIdentifier(name)
			require.NoError(t, err)
			_, err = database.ExecContext(t.Context(), formatG5Create(tc.create, quoted))
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = database.ExecContext(context.Background(), "DROP TABLE "+quoted) })
			_, err = database.ExecContext(t.Context(), "INSERT INTO "+quoted+" (id, value, count) VALUES (1, 9, 3)")
			require.NoError(t, err)
			db, err := rasql.New(database, tc.dialect)
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, tc.profile)
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			table := rasql.MustTableOf[g5LiveRow](schema.TableDef{Name: name, PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}}, {Name: "value", Type: schema.IntegerType{}, Default: "41"}, {Name: "count", Type: schema.IntegerType{}},
			}})
			id := query.TypedColumnOf[g5LiveRow, int64](table.Column("id"))
			value := query.TypedColumnOf[g5LiveRow, int64](table.Column("value"))
			count := query.TypedColumnOf[g5LiveRow, int64](table.Column("count"))
			plan, err := rasql.NewPatchPlan(table, query.EqualValue(id, int64(1)), rasql.DefaultField(value), rasql.SetField(count, int64(8)))
			require.NoError(t, err)
			outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
			require.Equal(t, int64(1), outcome.Affected)
			var gotValue, gotCount int64
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT value, count FROM "+quoted+" WHERE id = 1").Scan(&gotValue, &gotCount))
			require.Equal(t, int64(41), gotValue)
			require.Equal(t, int64(8), gotCount)
		})
	}
}

func formatG5Create(format, name string) string {
	for i := 0; i+1 < len(format); i++ {
		if format[i] == '%' && format[i+1] == 's' {
			return format[:i] + name + format[i+2:]
		}
	}
	return format
}
