//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestAggregateOrderingAgainstLiveDatabases proves the ORDER BY rule against the
// two dialects SQLite cannot speak for. PostgreSQL and MySQL treat an aggregate
// statement without GROUP BY as one group, so ordering by an aggregate runs
// while ordering by a bare column is refused -- the asymmetry validation now
// enforces. TestSQLiteOrdersAnAggregateStatement covers the same two shapes
// against SQLite, which runs both and therefore cannot prove the rejection.
// Each case skips when its server is unavailable; CI's integration job runs
// both.
func TestAggregateOrderingAgainstLiveDatabases(t *testing.T) {
	for _, test := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
		// bareColumnSQL spells, in this dialect's quoting, the statement the
		// builder used to render and no longer does.
		bareColumnSQL func(table string) string
	}{
		{
			name:    "postgresql",
			open:    dbtest.PostgreSQLDB,
			dialect: dialect.PostgreSQL(),
			bareColumnSQL: func(table string) string {
				return `SELECT COUNT(*) FROM "` + table + `" ORDER BY "` + table + `"."id"`
			},
		},
		{
			name:    "mysql",
			open:    dbtest.MySQLDB,
			dialect: dialect.MySQL(),
			bareColumnSQL: func(table string) string {
				return "SELECT COUNT(*) FROM `" + table + "` ORDER BY `" + table + "`.`id`"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testAggregateOrdering(t, test.open(t), test.dialect, test.bareColumnSQL)
		})
	}
}

func testAggregateOrdering(t *testing.T, database *sql.DB, d dialect.Dialect, bareColumnSQL func(string) string) {
	client, err := rasql.New(database, d)
	require.NoError(t, err)
	type record struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	// A per-run unique name keeps this test from ever dropping a table it did
	// not create, for the reason testDatabaseIntegration records.
	tableName := dbtest.UniqueName(t, "rasql_aggregate_order_records")
	definition := schema.Table{
		Name: tableName,
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	records, err := rasql.NewTable[record](definition)
	require.NoError(t, err)

	_, err = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
		require.NoError(t, err)
	}()
	require.NoError(t, rasql.Create(t.Context(), client, records))
	for _, fixture := range []record{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "grace@example.com"},
	} {
		_, err = rasql.Insert(t.Context(), client, records, fixture)
		require.NoError(t, err)
	}

	table, err := query.NewTable(definition)
	require.NoError(t, err)
	id, err := table.Column("id")
	require.NoError(t, err)
	counted := render.SelectFrom(d, table).Project(query.Project(query.CountAll()).As("count"))

	t.Run("the database runs an aggregate ordering", func(t *testing.T) {
		for name, builder := range map[string]render.SelectBuilder{
			"aggregate":               counted.Order(query.Asc(query.CountAll())),
			"aggregate over a column": counted.Order(query.Desc(query.Max(id))),
		} {
			t.Run(name, func(t *testing.T) {
				statement, err := builder.Build()
				require.NoError(t, err)
				var count int64
				result := database.QueryRowContext(t.Context(), statement.SQL(), statement.Args()...)
				require.NoError(t, result.Scan(&count))
				require.Equal(t, int64(2), count)
			})
		}
	})

	t.Run("the database refuses a bare-column ordering", func(t *testing.T) {
		// The database rejecting this SQL is why validation refuses to render
		// it: the ungrouped column belongs to no row of the single group.
		require.Error(t, runStatement(t, database, bareColumnSQL(tableName)))

		_, err := counted.Order(query.Asc(id)).Build()
		var validationErr *query.ValidationError
		require.ErrorAs(t, err, &validationErr)
	})
}
