package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestSelectRendersOrderByResultAlias pins the exact SQL query.AscResult and
// query.DescResult render on all three built-in dialects: the alias goes
// through Dialect.QuoteIdentifier exactly as the projection's own AS name
// does, so a mixed-case alias in ORDER BY matches the mixed-case name the
// projection was given, quoted the same way on each dialect.
func TestSelectRendersOrderByResultAlias(t *testing.T) {
	tests := map[string]struct {
		dialect    dialect.Dialect
		ascending  string
		descending string
	}{
		"postgresql": {
			dialect:    dialect.PostgreSQL(),
			ascending:  `SELECT "users"."id", "users"."name" AS "sortKey" FROM "users" ORDER BY "sortKey"`,
			descending: `SELECT "users"."id", "users"."name" AS "sortKey" FROM "users" ORDER BY "sortKey" DESC`,
		},
		"mysql": {
			dialect:    dialect.MySQL(),
			ascending:  "SELECT `users`.`id`, `users`.`name` AS `sortKey` FROM `users` ORDER BY `sortKey`",
			descending: "SELECT `users`.`id`, `users`.`name` AS `sortKey` FROM `users` ORDER BY `sortKey` DESC",
		},
		"sqlite": {
			dialect:    dialect.SQLite(),
			ascending:  `SELECT "users"."id", "users"."name" AS "sortKey" FROM "users" ORDER BY "sortKey"`,
			descending: `SELECT "users"."id", "users"."name" AS "sortKey" FROM "users" ORDER BY "sortKey" DESC`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			table, id, name := orderResultAliasTable(t)
			sortKey := name.As("sortKey")

			ascending, err := query.NewSelect(table, id, sortKey)
			require.NoError(t, err)
			ascending, err = ascending.WithOrder(query.AscResult(sortKey))
			require.NoError(t, err)
			renderedAscending, err := render.Select(test.dialect, ascending)
			require.NoError(t, err)
			require.Equal(t, test.ascending, renderedAscending.SQL())
			require.Empty(t, renderedAscending.Args(), "an alias order term binds no argument")

			descending, err := query.NewSelect(table, id, sortKey)
			require.NoError(t, err)
			descending, err = descending.WithOrder(query.DescResult(sortKey))
			require.NoError(t, err)
			renderedDescending, err := render.Select(test.dialect, descending)
			require.NoError(t, err)
			require.Equal(t, test.descending, renderedDescending.SQL())
			require.Empty(t, renderedDescending.Args(), "an alias order term binds no argument")
		})
	}
}

// TestSelectRendersOrderByResultAliasBesideLimitAndOffset pins that an alias
// order term contributes no placeholder of its own, so LIMIT and OFFSET
// number their placeholders exactly as they would with no ORDER BY at all.
func TestSelectRendersOrderByResultAliasBesideLimitAndOffset(t *testing.T) {
	table, id, name := orderResultAliasTable(t)
	sortKey := name.As("sortKey")

	statement, err := query.NewSelect(table, id, sortKey)
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.AscResult(sortKey))
	require.NoError(t, err)
	statement, err = statement.WithLimit(10)
	require.NoError(t, err)
	statement, err = statement.WithOffset(5)
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "users"."id", "users"."name" AS "sortKey" FROM "users" ORDER BY "sortKey" LIMIT $1 OFFSET $2`,
		rendered.SQL())
	require.Equal(t, []any{10, 5}, rendered.Args())
}

// orderResultAliasTable returns a users table with an id and a name column,
// so a test can project name under a result alias and order by that alias.
func orderResultAliasTable(t *testing.T) (query.TableRef, query.ColumnRef, query.ColumnRef) {
	t.Helper()
	table, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return table, table.Column("id"), table.Column("name")
}
