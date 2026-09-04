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

// orderResultAliasCase describes one live server decision 2 of
// order-by-alias-design.md is proved against: PostgreSQL and MySQL both
// refuse ORDER BY on a result name more than one projection reports, which is
// the premise query/validate.go's validateOrderResultAlias rests on.
type orderResultAliasCase struct {
	name    string
	open    func(*testing.T) *sql.DB
	dialect dialect.Dialect
	// ambiguousError is the substring the server's own error carries for
	// "ORDER BY id is ambiguous", stated once per engine because the two
	// engines word it differently.
	ambiguousError string
}

// TestOrderResultAliasAgainstLiveDatabases proves both halves of decision 2:
// a rasql-rendered statement ordering by a projection's result alias returns
// rows in the order that alias sorts by, and the ambiguous statement rasql's
// own validation refuses in Go is a statement the server itself would also
// have refused, with the exact wording query/validate.go's error message
// tells a reader to expect. Each case skips when its server is unavailable;
// CI's integration job runs both.
func TestOrderResultAliasAgainstLiveDatabases(t *testing.T) {
	for _, test := range []orderResultAliasCase{
		{
			name:           "postgresql",
			open:           dbtest.PostgreSQLDB,
			dialect:        dialect.PostgreSQL(),
			ambiguousError: "is ambiguous",
		},
		{
			name:           "mysql",
			open:           dbtest.MySQLDB,
			dialect:        dialect.MySQL(),
			ambiguousError: "ambiguous",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testOrderResultAlias(t, test.open(t), test)
		})
	}
}

func testOrderResultAlias(t *testing.T, database *sql.DB, test orderResultAliasCase) {
	db, err := rasql.New(database, test.dialect)
	require.NoError(t, err)
	type record struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
	}
	// A per-run unique name keeps this test from ever dropping a table it did
	// not create, for the reason dbtest.UniqueName's own doc records.
	tableName := dbtest.UniqueName(t, "rasql_order_result_alias_records")
	definition := schema.TableDef{
		Name: tableName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "city", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	records, err := rasql.TableOf[record](definition)
	require.NoError(t, err)

	_, err = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
		require.NoError(t, err)
	}()
	require.NoError(t, rasql.CreateTable(t.Context(), db, records))
	for _, fixture := range []record{
		{ID: 1, City: "tokyo"},
		{ID: 2, City: "osaka"},
		{ID: 3, City: "tokyo"},
	} {
		_, err = rasql.Insert(t.Context(), db, records, fixture)
		require.NoError(t, err)
	}

	table, err := query.NewTableRef(definition)
	require.NoError(t, err)
	location := table.Column("city").As("location")

	t.Run("the database runs a statement ordered by the projection's result alias", func(t *testing.T) {
		statement, err := query.NewSelect(table, location)
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.AscResult(location))
		require.NoError(t, err)
		rendered, err := render.Select(test.dialect, statement)
		require.NoError(t, err)

		var cities []string
		rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			cities = append(cities, c)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []string{"osaka", "tokyo", "tokyo"}, cities)
	})

	t.Run("the server refuses the ambiguous statement rasql's own validation also refuses", func(t *testing.T) {
		// rasql will not build this statement at all: query.NewSelect followed
		// by WithOrder(query.AscResult(city.As("id"))) fails validation in Go,
		// with the same word "ambiguous" this test's raw SQL proves the server
		// itself uses (query/order_result_alias_test.go pins the Go-side
		// refusal).
		// Sent as raw SQL because there is no rasql statement to render.
		ambiguous := "SELECT id, city AS id FROM " + tableName + " ORDER BY id"
		_, err := database.ExecContext(t.Context(), ambiguous)
		require.Error(t, err, "%s must refuse an ORDER BY term naming a result more than one projection reports", test.name)
		require.ErrorContains(t, err, test.ambiguousError)
	})
}
