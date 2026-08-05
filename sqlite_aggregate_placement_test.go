package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestSQLiteRefusesMisplacedAggregates proves the aggregate placement rules
// against a real database. The first half runs SQL of each misplaced shape
// against SQLite: three statements are refused outright, and the fourth is
// answered from an arbitrary row. The second half builds the same shapes
// through the public render builder and requires validation to refuse them, so
// none of that SQL is rendered at all.
func TestSQLiteRefusesMisplacedAggregates(t *testing.T) {
	database, definition := aggregatePlacementFixture(t)

	t.Run("sqlite refuses the SQL", func(t *testing.T) {
		tests := map[string]string{
			"where":            `SELECT "users"."id" FROM "users" WHERE (COUNT("users"."id") > 1)`,
			"order by":         `SELECT "users"."id" FROM "users" ORDER BY COUNT("users"."id")`,
			"nested aggregate": `SELECT SUM(SUM("users"."id")) FROM "users"`,
		}
		for name, statement := range tests {
			t.Run(name, func(t *testing.T) {
				err := runStatement(t, database, statement)
				require.ErrorContains(t, err, "aggregate")
			})
		}
	})

	t.Run("sqlite answers the mixed projection from an arbitrary row", func(t *testing.T) {
		// SQLite runs this one instead of refusing it, which is why validation
		// has to: it pairs the count of every row with the id of whichever row
		// it happened to keep, answering no question the caller asked.
		var id, count int64
		result := database.QueryRowContext(t.Context(), `SELECT "users"."id", COUNT(*) FROM "users"`)
		require.NoError(t, result.Scan(&id, &count))
		require.Equal(t, int64(3), count)
		require.Contains(t, []int64{1, 2, 3}, id)
	})

	t.Run("validation refuses to render them", func(t *testing.T) {
		table, err := query.NewTable(definition)
		require.NoError(t, err)
		id, err := table.Column("id")
		require.NoError(t, err)
		base := render.SelectFrom(dialect.SQLite(), table)

		tests := map[string]render.SelectBuilder{
			"where":                               base.Select("id").Where(query.GreaterThan(query.Count(id), query.Bind(1))),
			"order by beside a column projection": base.Select("id").Order(query.Asc(query.Count(id))),
			"nested aggregate":                    base.Project(query.Project(query.Sum(query.Sum(id)))),
			"mixed projections":                   base.Select("id").Project(query.Project(query.CountAll())),
		}
		for name, builder := range tests {
			t.Run(name, func(t *testing.T) {
				statement, err := builder.Build()
				var validationErr *query.ValidationError
				require.ErrorAs(t, err, &validationErr)
				require.Empty(t, statement.SQL(), "a refused statement renders no SQL")
			})
		}
	})
}

// TestSQLiteOrdersAnAggregateStatement proves the ORDER BY rule that follows the
// projection set, in both directions, against a real database. An
// aggregate-only projection ordered by an aggregate is legal SQL, so the builder
// has to render it and SQLite has to answer it. The same projection ordered by a
// bare column reads a column of no particular row, which PostgreSQL and MySQL
// both refuse and SQLite silently answers, so validation refuses it before it
// renders.
func TestSQLiteOrdersAnAggregateStatement(t *testing.T) {
	database, definition := aggregatePlacementFixture(t)

	table, err := query.NewTable(definition)
	require.NoError(t, err)
	id, err := table.Column("id")
	require.NoError(t, err)
	// Every builder below projects the same aggregate-only set, so only the
	// ordering differs between the accepted and the refused shapes.
	counted := render.SelectFrom(dialect.SQLite(), table).Project(query.Project(query.CountAll()).As("count"))

	t.Run("sqlite runs an aggregate ordering", func(t *testing.T) {
		tests := map[string]render.SelectBuilder{
			"aggregate":                  counted.Order(query.Asc(query.CountAll())),
			"aggregate over a column":    counted.Order(query.Desc(query.Max(id))),
			"expression over aggregates": counted.Order(query.Asc(query.GreaterThan(query.Count(id), query.Bind(1)))),
			"bound value":                counted.Order(query.Asc(query.Bind(1))),
		}
		for name, builder := range tests {
			t.Run(name, func(t *testing.T) {
				statement, err := builder.Build()
				require.NoError(t, err)
				var count int64
				result := database.QueryRowContext(t.Context(), statement.SQL(), statement.Args()...)
				require.NoError(t, result.Scan(&count))
				require.Equal(t, int64(3), count)
			})
		}
	})

	t.Run("sqlite answers a bare-column ordering from an arbitrary row", func(t *testing.T) {
		// SQLite runs this one instead of refusing it, which is why validation
		// has to: it orders the single aggregate row by the id of whichever row
		// SQLite happened to keep. PostgreSQL and MySQL reject the same SQL.
		var count int64
		result := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM "users" ORDER BY "users"."id"`)
		require.NoError(t, result.Scan(&count))
		require.Equal(t, int64(3), count)
	})

	t.Run("validation refuses to render a bare-column ordering", func(t *testing.T) {
		tests := map[string]render.SelectBuilder{
			"query column":               counted.Order(query.Asc(id)),
			"convenience helper":         counted.OrderAsc("id"),
			"column beside an aggregate": counted.Order(query.Asc(query.GreaterThan(query.Count(id), id))),
		}
		for name, builder := range tests {
			t.Run(name, func(t *testing.T) {
				statement, err := builder.Build()
				var validationErr *query.ValidationError
				require.ErrorAs(t, err, &validationErr)
				require.ErrorContains(t, err, "reads a column outside an aggregate function while the projections aggregate")
				require.Empty(t, statement.SQL(), "a refused statement renders no SQL")
			})
		}
	})
}

// aggregatePlacementFixture opens an in-memory SQLite database holding three
// users, and returns it with the table descriptor the placement tests build
// statements from.
func aggregatePlacementFixture(t *testing.T) (*sql.DB, schema.Table) {
	t.Helper()

	definition := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}

	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	// An in-memory SQLite database is per connection, so keep the test on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](definition)
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, users))
	for _, fixture := range []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		_, err = rasql.Insert(t.Context(), client, users, fixture)
		require.NoError(t, err)
	}
	return database, definition
}

// runStatement executes statement and returns the error the database reports,
// draining any result set so an error raised while stepping is not missed.
func runStatement(t *testing.T, database *sql.DB, statement string) error {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), statement)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}
