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
	// nonAggregateHavingSQL spells, in this dialect's quoting, a HAVING clause
	// on a query whose projections do not aggregate. Both servers refuse it,
	// which is what proves the model's HAVING-without-GROUP-BY rule matches
	// SQL rather than being stricter than it needs to be.
	nonAggregateHavingSQL func(table string) string
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
			nonAggregateHavingSQL: func(table string) string {
				return `SELECT "` + table + `"."id" FROM "` + table + `" HAVING ("` + table + `"."id" > 1)`
			},
		},
		{
			name:    "mysql",
			open:    dbtest.MySQLDB,
			dialect: dialect.MySQL(),
			bareColumnSQL: func(table string) string {
				return "SELECT COUNT(*) FROM `" + table + "` ORDER BY `" + table + "`.`id`"
			},
			nonAggregateHavingSQL: func(table string) string {
				return "SELECT `" + table + "`.`id` FROM `" + table + "` HAVING (`" + table + "`.`id` > 1)"
			},
			// This case leaves serverRefusesBareColumn false on measured
			// behavior: MySQL 8.4.11, from the mysql:8.4 image CI uses, with
			// ONLY_FULL_GROUP_BY in its sql_mode, ran the statement above and
			// returned a count. That same server rejected the mixed projection
			// SELECT COUNT(*), t.id FROM t with error 1140, and rejected this
			// ordering once the query named an explicit GROUP BY, with error
			// 1055.
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

	t.Run("the database runs a grouped mixed projection and refuses HAVING on a non-aggregate query", func(t *testing.T) {
		// SQLite cannot speak for either half of this: TestSQLiteRunsGroupedStatements
		// covers the grouped-mixed-projection half, and TestSQLiteRefusesMisplacedAggregates
		// records that SQLite refuses the misplaced shapes it does refuse, but
		// HAVING without GROUP BY on a non-aggregate query is refused by
		// PostgreSQL and MySQL specifically, which is the rule WithHaving matches.
		email, err := table.Column("email")
		require.NoError(t, err)
		grouped := render.SelectFrom(test.dialect, table).
			Project(query.Project(email), query.Project(query.CountAll()).As("count")).
			GroupBy(email)
		statement, err := grouped.Build()
		require.NoError(t, err)

		rows, err := database.QueryContext(t.Context(), statement.SQL(), statement.Args()...)
		require.NoError(t, err)
		defer rows.Close()
		seen := map[string]int64{}
		for rows.Next() {
			var gotEmail string
			var count int64
			require.NoError(t, rows.Scan(&gotEmail, &count))
			seen[gotEmail] = count
		}
		require.NoError(t, rows.Err())
		require.Len(t, seen, 2, "two fixtures, each with a distinct email, form two groups")

		err = runStatement(t, database, test.nonAggregateHavingSQL(tableName))
		require.Error(t, err, "both servers refuse a HAVING clause on a query whose projections do not aggregate")
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
