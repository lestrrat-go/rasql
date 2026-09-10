package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteAggregates(t *testing.T) {
	// TestSQLiteAggregates/"refuses a misplaced aggregate" proves the aggregate placement rules
	// against a real database. The first half runs SQL of each misplaced shape
	// against SQLite: four statements are refused outright, including a membership
	// test whose value list calls an aggregate, and the mixed projection is answered
	// from an arbitrary row instead. The second half builds the same shapes
	// through the public render builder and requires validation to refuse them, so
	// none of that SQL is rendered at all.
	t.Run("refuses a misplaced aggregate", func(t *testing.T) {
		database, definition := aggregatePlacementFixture(t)

		t.Run("sqlite refuses the SQL", func(t *testing.T) {
			tests := map[string]string{
				"where":            `SELECT "users"."id" FROM "users" WHERE (COUNT("users"."id") > 1)`,
				"where membership": `SELECT "users"."id" FROM "users" WHERE ("users"."id" IN (COUNT("users"."id")))`,
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
			table, err := query.NewTableRef(definition)
			require.NoError(t, err)
			id := table.Column("id")
			base := render.SelectFrom(dialect.SQLite(), table)

			tests := map[string]render.SelectBuilder{
				"where":                               base.Select("id").Where(query.GreaterThan(query.Count(id), query.Bind(1))),
				"where membership":                    base.Select("id").Where(query.In(id, query.Count(id))),
				"order by beside a column projection": base.Select("id").Order(query.Asc(query.Count(id))),
				"nested aggregate":                    base.Project(query.Sum(query.Sum(id))),
				"mixed projections":                   base.Select("id").Project(query.CountAll()),
				// A scalar function carries ctx unchanged into its arguments, so an
				// aggregate wrapped inside one is refused in WHERE exactly as a bare
				// aggregate is: the scalar call is not an exemption from the
				// placement rule.
				"scalar function wrapping an aggregate in where": base.Select("id").Where(query.GreaterThan(query.Coalesce(query.Count(id), query.Bind(0)), query.Bind(1))),
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
	})

	// TestSQLiteAggregates/"orders an aggregate statement" proves the ORDER BY rule that follows the
	// projection set, in both directions, against a real database. An
	// aggregate-only projection ordered by an aggregate is legal SQL, so the builder
	// has to render it and SQLite has to answer it. The same projection ordered by a
	// bare column reads a column of no particular row, which PostgreSQL refuses
	// while SQLite silently answers, so validation refuses it before it renders.
	// TestAggregateOrderingAgainstLiveDatabases records what each live server does
	// with that second shape, MySQL included, which answers it as SQLite does.
	t.Run("orders an aggregate statement", func(t *testing.T) {
		database, definition := aggregatePlacementFixture(t)

		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		id := table.Column("id")
		// Every builder below projects the same aggregate-only set, so only the
		// ordering differs between the accepted and the refused shapes.
		counted := render.SelectFrom(dialect.SQLite(), table).Project(query.CountAll().As("count"))

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
			// SQLite happened to keep. PostgreSQL rejects the same SQL, which is
			// what makes refusing it the portable answer; MySQL 8.4 runs it.
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
	})

	// TestSQLiteAggregates/"runs grouped statements" proves the two shapes GROUP BY and HAVING
	// exist for, against a real database: a grouped mixed projection returns one
	// row per group with the right per-group counts, and a grouped HAVING filters
	// those groups. It turns the "sqlite answers the mixed projection from an
	// arbitrary row" subtest of TestSQLiteAggregates/"refuses a misplaced aggregate" into a pair:
	// one shape still refused ungrouped, the same shape run grouped.
	t.Run("runs grouped statements", func(t *testing.T) {
		database, definition := aggregatePlacementFixture(t)
		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		id := table.Column("id")
		email := table.Column("email")

		t.Run("a grouped mixed projection returns one row per group", func(t *testing.T) {
			statement, err := query.NewGroupedSelect(table, []query.Expression{email},
				email,
				query.CountAll().As("total"),
			)
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
			require.NoError(t, err)

			rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
			require.NoError(t, err)
			defer func() { _ = rows.Close() }()
			seen := map[string]int64{}
			for rows.Next() {
				var gotEmail string
				var total int64
				require.NoError(t, rows.Scan(&gotEmail, &total))
				seen[gotEmail] = total
			}
			require.NoError(t, rows.Err())
			require.Len(t, seen, 3, "three fixture users, each with a distinct email, form three groups")
			for _, total := range seen {
				require.Equal(t, int64(1), total)
			}
		})

		t.Run("a grouped HAVING filters groups", func(t *testing.T) {
			statement, err := query.NewGroupedSelect(table, []query.Expression{id},
				id,
				query.CountAll().As("total"),
			)
			require.NoError(t, err)
			statement, err = statement.WithHaving(query.GreaterThan(id, query.Bind(1)))
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
			require.NoError(t, err)

			rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
			require.NoError(t, err)
			defer func() { _ = rows.Close() }()
			var ids []int64
			for rows.Next() {
				var gotID, total int64
				require.NoError(t, rows.Scan(&gotID, &total))
				ids = append(ids, gotID)
				require.Equal(t, int64(1), total)
			}
			require.NoError(t, rows.Err())
			require.ElementsMatch(t, []int64{2, 3}, ids, "HAVING id > 1 keeps groups 2 and 3 and drops group 1")
		})
	})

	// TestSQLiteAggregates/"runs scalar functions beside aggregates" proves a scalar function call
	// runs beside an aggregate against a real database: LOWER(email) groups the
	// rows and COALESCE(SUM(id), 0) aggregates within each group, which is the
	// shape a scalar function wrapping an aggregate is refused in WHERE for
	// (TestSQLiteAggregates/"refuses a misplaced aggregate") and accepted in a projection for.
	t.Run("runs scalar functions beside aggregates", func(t *testing.T) {
		database, definition := aggregatePlacementFixture(t)
		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		id := table.Column("id")
		email := table.Column("email")

		statement, err := query.NewGroupedSelect(table, []query.Expression{query.Lower(email)},
			query.Lower(email).As("email"),
			query.Coalesce(query.Sum(id), query.Bind(0)).As("total"),
		)
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), statement)
		require.NoError(t, err)

		rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		seen := map[string]int64{}
		for rows.Next() {
			var gotEmail string
			var total int64
			require.NoError(t, rows.Scan(&gotEmail, &total))
			seen[gotEmail] = total
		}
		require.NoError(t, rows.Err())
		require.Equal(t, map[string]int64{
			"ada@example.com": 1,
			"bob@example.com": 2,
			"cyd@example.com": 3,
		}, seen)
	})
}

// aggregatePlacementFixture opens an in-memory SQLite database holding three
// users, and returns it with the table descriptor the placement tests build
// statements from.
func aggregatePlacementFixture(t *testing.T) (*sql.DB, schema.TableDef) {
	t.Helper()

	definition := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
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

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](definition)
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	userID := query.TypedColumnOf[user, int64](users.Column("id"))
	userEmail := query.TypedColumnOf[user, string](users.Column("email"))
	for _, fixture := range []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		plan, err := rasql.NewCreatePlan(users, rasql.SetField(userID, fixture.ID), rasql.SetField(userEmail, fixture.Email))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
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
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	return rows.Err()
}

func TestSQLiteDistinct(t *testing.T) {
	// TestSQLiteDistinct/"runs distinct statements" proves SELECT DISTINCT executes against a
	// real database and de-duplicates result rows, including the distinct-then-
	// page shape a caller uses for a value list.
	t.Run("runs distinct statements", func(t *testing.T) {
		database, definition := distinctFixture(t)
		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		city := table.Column("city")

		t.Run("distinct removes duplicate rows", func(t *testing.T) {
			statement, err := query.NewSelect(table, city)
			require.NoError(t, err)
			statement, err = statement.WithDistinct()
			require.NoError(t, err)
			statement, err = statement.WithOrder(query.Asc(city))
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
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
			require.Equal(t, []string{"osaka", "tokyo"}, cities, "three fixture rows share two distinct cities")
		})

		t.Run("distinct then page keeps the deduplicated order", func(t *testing.T) {
			statement, err := query.NewSelect(table, city)
			require.NoError(t, err)
			statement, err = statement.WithDistinct()
			require.NoError(t, err)
			statement, err = statement.WithOrder(query.Asc(city))
			require.NoError(t, err)
			statement, err = statement.WithLimit(1)
			require.NoError(t, err)
			statement, err = statement.WithOffset(1)
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
			require.NoError(t, err)

			var c string
			result := database.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...)
			require.NoError(t, result.Scan(&c))
			require.Equal(t, "tokyo", c)
		})

		t.Run("COUNT(DISTINCT column) counts distinct values", func(t *testing.T) {
			statement, err := query.NewSelect(table, query.Count(city).WithDistinct().As("distinct_cities"))
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
			require.NoError(t, err)

			var count int64
			result := database.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...)
			require.NoError(t, result.Scan(&count))
			require.Equal(t, int64(2), count)
		})
	})

	// TestSQLiteDistinct/"a distinct count drops NULL" proves the divergence documented in
	// docs/core/02-sql-builder.md under Aggregates and docs/orm/03-typed-queries.md
	// under Select distinct rows:
	// COUNT(DISTINCT column) counts the distinct non-NULL values of one column,
	// while SELECT DISTINCT over that same column keeps NULL as a value of its
	// own. The two therefore answer differently over data holding a NULL, which is
	// why COUNT(DISTINCT column) is not a count of the rows SELECT DISTINCT
	// returns.
	t.Run("a distinct count drops NULL", func(t *testing.T) {
		definition := schema.TableDef{
			Name: "visits",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "city", Type: schema.TextType{}, Nullable: true},
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

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		type visit struct {
			ID   int64   `rasql:"id"`
			City *string `rasql:"city"`
		}
		visits, err := rasql.TableOf[visit](definition)
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, visits))
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		visitID := query.TypedColumnOf[visit, int64](visits.Column("id"))
		visitCity := query.NullableColumnOf[visit, string](visits.Column("city"))
		tokyo := "tokyo"
		// NULL, NULL, tokyo: two distinct rows, one distinct non-NULL value.
		for _, fixture := range []visit{
			{ID: 1, City: nil},
			{ID: 2, City: nil},
			{ID: 3, City: &tokyo},
		} {
			fields := []rasql.MutationField[visit]{rasql.SetField(visitID, fixture.ID)}
			if fixture.City != nil {
				fields = append(fields, rasql.SetNullableField(visitCity, *fixture.City))
			} else {
				fields = append(fields, rasql.ClearField(visitCity))
			}
			plan, err := rasql.NewCreatePlan(visits, fields...)
			require.NoError(t, err)
			_, err = rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
		}

		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		city := table.Column("city")

		t.Run("SELECT DISTINCT keeps NULL as a value", func(t *testing.T) {
			statement, err := query.NewSelect(table, city)
			require.NoError(t, err)
			statement, err = statement.WithDistinct()
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
			require.NoError(t, err)

			var cities []*string
			rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
			require.NoError(t, err)
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var c *string
				require.NoError(t, rows.Scan(&c))
				cities = append(cities, c)
			}
			require.NoError(t, rows.Err())
			require.Len(t, cities, 2, "NULL and tokyo are two distinct rows")
		})

		t.Run("COUNT(DISTINCT column) drops NULL", func(t *testing.T) {
			statement, err := query.NewSelect(table, query.Count(city).WithDistinct().As("distinct_cities"))
			require.NoError(t, err)
			rendered, err := render.Select(dialect.SQLite(), statement)
			require.NoError(t, err)

			var count int64
			result := database.QueryRowContext(t.Context(), rendered.SQL(), rendered.Args()...)
			require.NoError(t, result.Scan(&count))
			require.Equal(t, int64(1), count, "the two NULL rows contribute no value to count")
		})
	})

	// TestSQLiteDistinct/"an unprojected distinct order is answered arbitrarily" proves the rationale
	// behind the maintainer decision documented at query/select.go's WithDistinct:
	// rasql renders a DISTINCT statement ordered by a column outside its
	// projections rather than refusing it in Go, because SQLite runs it and
	// answers from whichever row survived de-duplication -- no question the
	// caller asked. live_select_integration_test.go proves that PostgreSQL and
	// MySQL instead refuse the same shape at the server, with SQLSTATE 42P10 and
	// error 3065.
	t.Run("an unprojected distinct order is answered arbitrarily", func(t *testing.T) {
		database, definition := distinctFixture(t)
		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		city := table.Column("city")
		age := table.Column("age")

		// The builder renders this statement rather than refusing it: rasql places
		// no Go-side rule on which ORDER BY expressions a distinct statement may
		// use.
		statement, err := query.NewSelect(table, city)
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(age))
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), statement)
		require.NoError(t, err)
		require.Equal(t, `SELECT DISTINCT "`+definition.Name+`"."city" FROM "`+definition.Name+`" ORDER BY "`+definition.Name+`"."age"`, rendered.SQL())

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
		// SQLite answers the statement rather than refusing it, in whichever order
		// its query planner happens to keep the surviving rows -- the point being
		// proven is that it runs at all, not which order it picks.
		require.ElementsMatch(t, []string{"osaka", "tokyo"}, cities)
	})
}

// distinctFixture opens an in-memory SQLite database holding three users
// across two cities and distinct ages, so DISTINCT city has a real duplicate
// to remove and ORDER BY age has a real per-row difference from ORDER BY city.
func distinctFixture(t *testing.T) (*sql.DB, schema.TableDef) {
	t.Helper()

	definition := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "city", Type: schema.TextType{}},
			{Name: "age", Type: schema.IntegerType{}},
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

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
		Age  int64  `rasql:"age"`
	}
	users, err := rasql.TableOf[user](definition)
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	userID := query.TypedColumnOf[user, int64](users.Column("id"))
	userCity := query.TypedColumnOf[user, string](users.Column("city"))
	userAge := query.TypedColumnOf[user, int64](users.Column("age"))
	for _, fixture := range []user{
		{ID: 1, City: "tokyo", Age: 30},
		{ID: 2, City: "osaka", Age: 20},
		{ID: 3, City: "tokyo", Age: 10},
	} {
		plan, err := rasql.NewCreatePlan(users,
			rasql.SetField(userID, fixture.ID),
			rasql.SetField(userCity, fixture.City),
			rasql.SetField(userAge, fixture.Age),
		)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}
	return database, definition
}

func TestSQLiteOrderResultAlias(t *testing.T) {
	// TestSQLiteOrderResultAlias/"orders by a result alias" proves query.AscResult/DescResult
	// executes against a real database, rather than only validating and
	// rendering: the row order it returns is the order the aliased projection
	// sorts by, not the order the underlying table happens to store rows in.
	t.Run("orders by a result alias", func(t *testing.T) {
		database, definition := orderResultAliasFixture(t)
		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		location := table.Column("city").As("location")

		statement, err := query.NewSelect(table, location)
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.AscResult(location))
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), statement)
		require.NoError(t, err)
		require.Equal(t,
			`SELECT "`+definition.Name+`"."city" AS "location" FROM "`+definition.Name+`" ORDER BY "location"`,
			rendered.SQL())

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
		// The fixture inserts tokyo, osaka, tokyo (see orderResultAliasFixture),
		// so ordering by the "location" alias ascending sorts osaka before tokyo.
		require.Equal(t, []string{"osaka", "tokyo", "tokyo"}, cities)

		descending, err := query.NewSelect(table, location)
		require.NoError(t, err)
		descending, err = descending.WithOrder(query.DescResult(location))
		require.NoError(t, err)
		renderedDescending, err := render.Select(dialect.SQLite(), descending)
		require.NoError(t, err)

		var descendingCities []string
		rows, err = database.QueryContext(t.Context(), renderedDescending.SQL(), renderedDescending.Args()...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c string
			require.NoError(t, rows.Scan(&c))
			descendingCities = append(descendingCities, c)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []string{"tokyo", "tokyo", "osaka"}, descendingCities)
	})

	// TestSQLiteOrderResultAlias/"refuses an ambiguous result alias" proves decision 2's premise on
	// the one dialect that would otherwise hide the problem: SQLite silently
	// resolves "SELECT id, name AS id FROM u ORDER BY id" to name, rather than
	// erroring the way PostgreSQL and MySQL do (see
	// live_select_integration_test.go for both of those). rasql refuses
	// to build the statement at all, in Go, before it ever reaches a database
	// that would answer a question the caller did not ask.
	t.Run("refuses an ambiguous result alias", func(t *testing.T) {
		_, definition := orderResultAliasFixture(t)
		table, err := query.NewTableRef(definition)
		require.NoError(t, err)
		id := table.Column("id")
		aliasedCity := table.Column("city").As("id")

		statement, err := query.NewSelect(table, id, aliasedCity)
		require.NoError(t, err)
		_, err = statement.WithOrder(query.AscResult(aliasedCity))
		require.Error(t, err)
		require.ErrorContains(t, err, "ambiguous")
	})
}

// orderResultAliasFixture opens an in-memory SQLite database holding three
// rows across two cities, so an alias ordering has a real duplicate value to
// sort and a real per-row difference to observe.
func orderResultAliasFixture(t *testing.T) (*sql.DB, schema.TableDef) {
	t.Helper()

	definition := schema.TableDef{
		Name: "order_result_alias_people",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "city", Type: schema.TextType{}},
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

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type person struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
	}
	people, err := rasql.TableOf[person](definition)
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, people))
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	personID := query.TypedColumnOf[person, int64](people.Column("id"))
	personCity := query.TypedColumnOf[person, string](people.Column("city"))
	for _, fixture := range []person{
		{ID: 1, City: "tokyo"},
		{ID: 2, City: "osaka"},
		{ID: 3, City: "tokyo"},
	} {
		plan, err := rasql.NewCreatePlan(people, rasql.SetField(personID, fixture.ID), rasql.SetField(personCity, fixture.City))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}
	return database, definition
}

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
			from   query.TableRef
			joined query.TableRef
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
				from:   query.MustTableRef(ambiguousSourceUsers("")),
				joined: query.MustTableRef(ambiguousSourceUsers("tenant_a")),
			},
			"qualified table joined to an unqualified one of the same name": {
				from:   query.MustTableRef(ambiguousSourceUsers("tenant_a")),
				joined: query.MustTableRef(ambiguousSourceUsers("")),
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
			from   query.TableRef
			joined query.TableRef
		}{
			"same name in two schemas, both unaliased": {
				from:   query.MustTableRef(ambiguousSourceUsers("tenant_a")),
				joined: query.MustTableRef(ambiguousSourceUsers("tenant_b")),
			},
			"same name in two schemas under distinct aliases": {
				from:   ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_a"), "a"),
				joined: ambiguousSourceAlias(t, ambiguousSourceUsers("tenant_b"), "b"),
			},
			"one table joined to itself under a distinct alias": {
				from:   query.MustTableRef(ambiguousSourceUsers("")),
				joined: ambiguousSourceAlias(t, ambiguousSourceUsers(""), "manager"),
			},
			"an alias separates the two sources the rule refused unaliased": {
				from:   query.MustTableRef(ambiguousSourceUsers("")),
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

func ambiguousSourceUsers(schemaName string) schema.TableDef {
	return schema.TableDef{
		Schema: schemaName,
		Name:   "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

func ambiguousSourceOrders() schema.TableDef {
	return schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
}

func ambiguousSourceAlias(t *testing.T, definition schema.TableDef, alias string) query.TableRef {
	t.Helper()
	table, err := query.MustTableRef(definition).As(alias)
	require.NoError(t, err)
	return table
}

// ambiguousSourceJoin renders an inner join of from and joined on their id
// columns, and returns whatever render.Select produced so a caller can require
// that a refused statement carries no SQL.
func ambiguousSourceJoin(t *testing.T, from query.TableRef, joined query.TableRef) (stmt.Statement, error) {
	t.Helper()
	fromID := from.Column("id")
	joinedID := joined.Column("id")

	s, err := query.NewJoinedSelect(from,
		[]query.Join{query.InnerJoin(joined, query.Equal(fromID, joinedID))},
		nil,
		fromID,
	)
	if err != nil {
		return stmt.Statement{}, err
	}
	return render.Select(dialect.SQLite(), s)
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

func TestSQLiteRejectsCaseOnlyCorrelationAlias(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO users (id) VALUES (1), (2)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO orders (id, user_id) VALUES (1, 1)`)
	require.NoError(t, err)

	users := query.MustTableRef(schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	})
	orders := query.MustTableRef(schema.TableDef{
		Name: "orders", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}}, {Name: "user_id", Type: schema.IntegerType{}},
		}, PrimaryKey: []string{"id"},
	})
	aliased, err := orders.As("USERS")
	require.NoError(t, err)
	inner, err := query.NewSelect(aliased, query.Project(query.Bind(1)))
	require.NoError(t, err)
	inner, err = inner.WithCorrelation(users)
	require.NoError(t, err)
	inner, err = inner.WithWhere(query.Equal(aliased.Column("user_id"), users.Column("id")))
	require.NoError(t, err)
	outer, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	outer, err = outer.WithWhere(query.Exists(inner))
	require.NoError(t, err)
	_, err = render.Select(dialect.SQLite(), outer)
	require.ErrorContains(t, err, "sqlite")
	require.ErrorContains(t, err, "USERS")
	require.ErrorContains(t, err, "users")
	require.ErrorContains(t, err, "distinct alias")

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	deleteStatement, err := query.NewDelete(users)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Exists(inner))
	require.NoError(t, err)
	deletePlan, err := rasql.NewStatementPlan(deleteStatement)
	require.NoError(t, err)
	outcome, err := rasql.ExecMutation(t.Context(), executor, deletePlan)
	require.ErrorContains(t, err, "distinct alias")
	require.Zero(t, outcome)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	require.Equal(t, 2, count)

	distinct, err := orders.As("o")
	require.NoError(t, err)
	inner, err = query.NewSelect(distinct, query.Project(query.Bind(1)))
	require.NoError(t, err)
	inner, err = inner.WithCorrelation(users)
	require.NoError(t, err)
	inner, err = inner.WithWhere(query.Equal(distinct.Column("user_id"), users.Column("id")))
	require.NoError(t, err)
	outer, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	outer, err = outer.WithWhere(query.Exists(inner))
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), outer)
	require.NoError(t, err)
	rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next())
	var id int64
	require.NoError(t, rows.Scan(&id))
	require.Equal(t, int64(1), id)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}
