package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestSQLiteRefusesAmbiguousSources proves the rule that two sources of one
// statement must render under names a server can tell apart, against a real
// database rather than a golden string. The first half runs the SQL each
// refused shape used to render and requires SQLite to reject it with
// "ambiguous column name", so the rule follows what a server does and not only
// what the rendered text looks like. The second half builds the same shapes
// through the public API and requires validation to refuse them before any SQL
// is rendered. The third half runs the shapes validation still accepts, so the
// rule refuses no statement a server answers.
func TestSQLiteRefusesAmbiguousSources(t *testing.T) {
	database := ambiguousSourceFixture(t)

	t.Run("sqlite refuses the SQL", func(t *testing.T) {
		tests := map[string]string{
			"two qualified tables share an alias":                           `SELECT "u"."id" FROM "tenant_a"."users" AS "u" INNER JOIN "tenant_b"."users" AS "u" ON ("u"."id" = "u"."id")`,
			"two unrelated tables share an alias":                           `SELECT "u"."id" FROM "users" AS "u" INNER JOIN "orders" AS "u" ON ("u"."id" = "u"."id")`,
			"unqualified table joined to a qualified one of the same name":  `SELECT "users"."id" FROM "users" INNER JOIN "tenant_a"."users" ON ("users"."id" = "tenant_a"."users"."id")`,
			"qualified table joined to an unqualified one of the same name": `SELECT "tenant_a"."users"."id" FROM "tenant_a"."users" INNER JOIN "users" ON ("tenant_a"."users"."id" = "users"."id")`,
		}
		for name, statement := range tests {
			t.Run(name, func(t *testing.T) {
				err := runStatement(t, database, statement)
				require.ErrorContains(t, err, "ambiguous column name")
			})
		}
	})

	t.Run("validation refuses to render them", func(t *testing.T) {
		tests := map[string]struct {
			from   query.Table
			joined query.Table
		}{
			"two qualified tables share an alias": {
				from:   ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_a"), "u"),
				joined: ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_b"), "u"),
			},
			"two unrelated tables share an alias": {
				from:   ambiguousSourceAlias(t, ambiguousSourceUsers(""), "u"),
				joined: ambiguousSourceAlias(t, ambiguousSourceOrders(), "u"),
			},
			"unqualified table joined to a qualified one of the same name": {
				from:   query.MustNewTable(ambiguousSourceUsers("")),
				joined: query.MustNewTable(ambiguousSourceUsers("tenant_a")),
			},
			"qualified table joined to an unqualified one of the same name": {
				from:   query.MustNewTable(ambiguousSourceUsers("tenant_a")),
				joined: query.MustNewTable(ambiguousSourceUsers("")),
			},
		}
		for name, testCase := range tests {
			t.Run(name, func(t *testing.T) {
				statement, err := ambiguousSourceJoin(t, testCase.from, testCase.joined)
				var validationErr *query.ValidationError
				require.ErrorAs(t, err, &validationErr)
				require.Empty(t, statement.SQL(), "a refused statement renders no SQL")
			})
		}
	})

	t.Run("sqlite runs the shapes validation still accepts", func(t *testing.T) {
		tests := map[string]struct {
			from   query.Table
			joined query.Table
		}{
			"same name in two schemas, both unaliased": {
				from:   query.MustNewTable(ambiguousSourceUsers("tenant_a")),
				joined: query.MustNewTable(ambiguousSourceUsers("tenant_b")),
			},
			"same name in two schemas under distinct aliases": {
				from:   ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_a"), "a"),
				joined: ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_b"), "b"),
			},
			"one table joined to itself under a distinct alias": {
				from:   query.MustNewTable(ambiguousSourceUsers("")),
				joined: ambiguousSourceAlias(t, ambiguousSourceUsers(""), "manager"),
			},
			"an alias separates the two sources the rule refused unaliased": {
				from:   query.MustNewTable(ambiguousSourceUsers("")),
				joined: ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_a"), "tenant"),
			},
		}
		for name, testCase := range tests {
			t.Run(name, func(t *testing.T) {
				statement, err := ambiguousSourceJoin(t, testCase.from, testCase.joined)
				require.NoError(t, err)
				require.NoError(t, runStatement(t, database, statement.SQL()))
			})
		}
	})
}

func ambiguousSourceUsers(schemaName string) schema.Table {
	return schema.Table{
		Schema: schemaName,
		Name:   "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

func ambiguousSourceOrders() schema.Table {
	return schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

func ambiguousSourceAlias(t *testing.T, definition schema.Table, alias string) query.Table {
	t.Helper()
	table, err := query.MustNewTable(definition).As(alias)
	require.NoError(t, err)
	return table
}

// ambiguousSourceJoin renders an inner join of from and joined on their id
// columns, and returns whatever render.Select produced so a caller can require
// that a refused statement carries no SQL.
func ambiguousSourceJoin(t *testing.T, from query.Table, joined query.Table) (render.Statement, error) {
	t.Helper()
	fromID, err := from.Column("id")
	require.NoError(t, err)
	joinedID, err := joined.Column("id")
	require.NoError(t, err)

	statement, err := query.NewJoinedSelect(from,
		[]query.Join{query.InnerJoin(joined, query.Equal(fromID, joinedID))},
		nil,
		query.Project(fromID),
	)
	if err != nil {
		return render.Statement{}, err
	}
	return render.Select(dialect.SQLite(), statement)
}

// ambiguousSourceFixture opens an in-memory SQLite database holding an
// unqualified users and orders alongside a users table in each of two attached
// databases, which is how SQLite spells the namespace a PostgreSQL schema or a
// MySQL database names. rasql renders no qualified DDL, so the tables are
// created through raw SQL, the same way a native migration would create them.
func ambiguousSourceFixture(t *testing.T) *sql.DB {
	t.Helper()

	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	// An in-memory SQLite database is per connection, and so is an attached
	// one, so keep the test on a single connection.
	database.SetMaxOpenConns(1)

	for _, statement := range []string{
		`ATTACH DATABASE ':memory:' AS tenant_a`,
		`ATTACH DATABASE ':memory:' AS tenant_b`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL)`,
		`CREATE TABLE tenant_a.users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`,
		`CREATE TABLE tenant_b.users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`,
	} {
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	return database
}
