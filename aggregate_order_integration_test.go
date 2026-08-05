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

// aggregateOrderingCase describes one live server the ORDER BY rule is proved
// against.
type aggregateOrderingCase struct {
	name    string
	open    func(*testing.T) *sql.DB
	dialect dialect.Dialect
	// bareColumnSQL spells, in this dialect's quoting, the statement the
	// builder used to render and no longer does.
	bareColumnSQL func(table string) string
	// serverRefusesBareColumn records whether this server rejects
	// bareColumnSQL. It is the one point the two servers disagree on, so the
	// test's doc comment carries the reason.
	serverRefusesBareColumn bool
}

// TestAggregateOrderingAgainstLiveDatabases proves the ORDER BY rule against the
// two dialects SQLite cannot speak for. PostgreSQL and MySQL both treat an
// aggregate statement without GROUP BY as one group, so both run an ordering by
// an aggregate. They part ways on an ordering by a bare column: PostgreSQL
// rejects the ungrouped column, while MySQL 8.4 runs the statement even with
// ONLY_FULL_GROUP_BY in its default sql_mode, so each case records the answer
// its own server gives. Validation refuses that statement for every dialect
// regardless, since only PostgreSQL's answer is portable.
// TestSQLiteOrdersAnAggregateStatement covers the same two shapes against
// SQLite, which runs both. Each case skips when its server is unavailable; CI's
// integration job runs both.
func TestAggregateOrderingAgainstLiveDatabases(t *testing.T) {
	for _, test := range []aggregateOrderingCase{
		{
			name:    "postgresql",
			open:    dbtest.PostgreSQLDB,
			dialect: dialect.PostgreSQL(),
			bareColumnSQL: func(table string) string {
				return `SELECT COUNT(*) FROM "` + table + `" ORDER BY "` + table + `"."id"`
			},
			serverRefusesBareColumn: true,
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
			testAggregateOrdering(t, test.open(t), test)
		})
	}
}

func testAggregateOrdering(t *testing.T, database *sql.DB, test aggregateOrderingCase) {
	client, err := rasql.New(database, test.dialect)
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
	counted := render.SelectFrom(test.dialect, table).Project(query.Project(query.CountAll()).As("count"))

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

	t.Run("the server answers a bare-column ordering its own way", func(t *testing.T) {
		// The two servers genuinely disagree here, so no single assertion
		// covers both. PostgreSQL rejects the ungrouped column, because it
		// belongs to no row of the single group. MySQL 8.4 runs the same
		// statement under its default sql_mode: ONLY_FULL_GROUP_BY checks an
		// ORDER BY list only when the query names a GROUP BY, and this one is
		// grouped implicitly by its aggregate, which leaves the select list as
		// the only list checked -- MySQL does reject the mixed projection
		// SELECT COUNT(*), t.id FROM t.
		err := runStatement(t, database, test.bareColumnSQL(tableName))
		if !test.serverRefusesBareColumn {
			require.NoError(t, err)
			return
		}
		require.Error(t, err)
	})

	t.Run("validation refuses to render a bare-column ordering", func(t *testing.T) {
		// Validation refuses it for both dialects, MySQL included: PostgreSQL
		// rejecting the statement is what makes refusal the portable answer,
		// and a builder that rendered it for MySQL alone would render SQL that
		// does not survive a move to PostgreSQL.
		_, err := counted.Order(query.Asc(id)).Build()
		var validationErr *query.ValidationError
		require.ErrorAs(t, err, &validationErr)
	})
}
