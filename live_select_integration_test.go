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
	// bareColumnSQL. It is one of the two points the two servers disagree on,
	// so the test's doc comment carries the reason.
	serverRefusesBareColumn bool
	// nonAggregateHavingSQL spells, in this dialect's quoting, a HAVING clause
	// on a query that names no GROUP BY and whose projections do not
	// aggregate. This is the second point the two servers disagree on.
	nonAggregateHavingSQL func(table string) string
	// serverRefusesNonAggregateHaving records whether this server rejects
	// nonAggregateHavingSQL. Neither server refuses a HAVING merely for
	// lacking a GROUP BY, so the answers differ and the subtest's doc comment
	// carries the reason.
	serverRefusesNonAggregateHaving bool
}

// TestAggregateOrderingAgainstLiveDatabases proves the ORDER BY rule against the
// two dialects SQLite cannot speak for. PostgreSQL and MySQL both treat an
// aggregate statement without GROUP BY as one group, so both run an ordering by
// an aggregate. They part ways on an ordering by a bare column: PostgreSQL
// rejects the ungrouped column, while MySQL 8.4 runs the statement even with
// ONLY_FULL_GROUP_BY in its default sql_mode, so each case records the answer
// its own server gives. Validation refuses that statement for every dialect
// regardless, since only PostgreSQL's answer is portable.
// TestSQLiteAggregates/"orders an aggregate statement" covers the same two shapes against
// SQLite, which runs both. Each case skips when its server is unavailable; CI's
// integration job runs both.
// The test also covers the grouped shapes GROUP BY and HAVING exist for, and a
// HAVING that names no GROUP BY, on which the two servers part ways a second
// time; the subtests carry those reasons.
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
			serverRefusesNonAggregateHaving: true,
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
			// It leaves serverRefusesNonAggregateHaving false on measured
			// behavior too: that same server ran nonAggregateHavingSQL and
			// returned rows.
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testAggregateOrdering(t, test.open(t), test)
		})
	}
}

func testAggregateOrdering(t *testing.T, database *sql.DB, test aggregateOrderingCase) {
	db, err := rasql.New(database, test.dialect)
	require.NoError(t, err)
	type record struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	// A per-run unique name keeps this test from ever dropping a table it did
	// not create, for the reason testDatabaseIntegration records.
	tableName := dbtest.UniqueName(t, "rasql_aggregate_order_records")
	definition := schema.TableDef{
		Name: tableName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
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

	profileID := "postgresql-17"
	if test.dialect.Name() == "mysql" {
		profileID = "mysql-8.4"
	}
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, profileID)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	recordID := query.TypedColumnOf[record, int64](records.Column("id"))
	recordEmail := query.TypedColumnOf[record, string](records.Column("email"))
	for _, fixture := range []record{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "grace@example.com"},
	} {
		plan, err := rasql.NewCreatePlan(records, rasql.SetField(recordID, fixture.ID), rasql.SetField(recordEmail, fixture.Email))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}

	table, err := query.NewTableRef(definition)
	require.NoError(t, err)
	id := table.Column("id")
	counted := render.SelectFrom(test.dialect, table).Project(query.CountAll().As("count"))

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

	t.Run("the database runs a grouped mixed projection", func(t *testing.T) {
		// TestSQLiteAggregates/"runs grouped statements" proves the same shape against
		// SQLite; this proves it against the two servers SQLite cannot speak
		// for.
		email := table.Column("email")
		grouped := render.SelectFrom(test.dialect, table).
			Project(email, query.CountAll().As("count")).
			GroupBy(email)
		statement, err := grouped.Build()
		require.NoError(t, err)

		rows, err := database.QueryContext(t.Context(), statement.SQL(), statement.Args()...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		seen := map[string]int64{}
		for rows.Next() {
			var gotEmail string
			var count int64
			require.NoError(t, rows.Scan(&gotEmail, &count))
			seen[gotEmail] = count
		}
		require.NoError(t, rows.Err())
		require.Len(t, seen, 2, "two fixtures, each with a distinct email, form two groups")
	})

	t.Run("the server answers a HAVING without GROUP BY its own way", func(t *testing.T) {
		// The two servers genuinely disagree here, so no single assertion
		// covers both, and neither one refuses the statement for the reason a
		// reader might expect: a HAVING that names no GROUP BY is not refused
		// as such by either server. PostgreSQL counts a HAVING clause as
		// making the query grouped, so the bare id in the select list belongs
		// to no row of the single group and the server rejects the statement.
		// MySQL 8.4 does not count it that way: with neither a GROUP BY nor an
		// aggregate the query is not grouped at all, so ONLY_FULL_GROUP_BY
		// checks nothing, and the server runs the HAVING as a row filter and
		// returns rows.
		//
		// SQLite gives a third answer: 3.53.3, the version the driver
		// this module builds against reports, refuses the same statement with
		// "HAVING clause on a non-aggregate query".
		//
		// So there is no portable "the servers refuse this" claim to assert,
		// and this subtest asserts only the answer each server actually gives.
		// rasql is deliberately stricter than both for the statement run here:
		// query refuses a HAVING on a statement that neither names a GROUP BY
		// nor aggregates in any projection, which is exactly this statement, so
		// a rasql caller never reaches either server with it and the SQL here is
		// spelled out by hand. A HAVING over a projection set that aggregates
		// and reads no column outside an aggregate is accepted without a GROUP
		// BY, because that set is one group; a projection reading no column, a
		// bound value for instance, may sit beside the aggregate in that set.
		// TestSelectRejectsInvalidHaving covers the refusal and
		// TestSelectAcceptsGroupedStatements covers the acceptance.
		err := runStatement(t, database, test.nonAggregateHavingSQL(tableName))
		if !test.serverRefusesNonAggregateHaving {
			require.NoError(t, err)
			return
		}
		require.Error(t, err)
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

// distinctOrderCase describes one live server the ORDER BY-vs-DISTINCT
// decision at query/select.go's WithDistinct is proved against.
type distinctOrderCase struct {
	name    string
	open    func(*testing.T) *sql.DB
	dialect dialect.Dialect
}

// TestDistinctOrderAgainstLiveDatabases proves the reason rasql leaves an
// unprojected ORDER BY on a distinct statement to the database rather than
// refusing it in Go: PostgreSQL and MySQL both refuse the shape at the
// server, with SQLSTATE 42P10 and error 3065 ER_FIELD_IN_ORDER_NOT_SELECT,
// so a rasql-rendered statement never reaches a silently wrong answer on
// either of them. TestSQLiteDistinct/"an unprojected distinct order is answered arbitrarily" covers
// the third dialect, which runs the same shape instead of refusing it. Each
// case skips when its server is unavailable; CI's integration job runs both.
func TestDistinctOrderAgainstLiveDatabases(t *testing.T) {
	for _, test := range []distinctOrderCase{
		{name: "postgresql", open: dbtest.PostgreSQLDB, dialect: dialect.PostgreSQL()},
		{name: "mysql", open: dbtest.MySQLDB, dialect: dialect.MySQL()},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDistinctOrder(t, test.open(t), test)
		})
	}
}

func testDistinctOrder(t *testing.T, database *sql.DB, test distinctOrderCase) {
	db, err := rasql.New(database, test.dialect)
	require.NoError(t, err)
	type record struct {
		ID   int64  `rasql:"id"`
		City string `rasql:"city"`
		Age  int64  `rasql:"age"`
	}
	// A per-run unique name keeps this test from ever dropping a table it did
	// not create, for the reason testDatabaseIntegration records.
	tableName := dbtest.UniqueName(t, "rasql_distinct_order_records")
	definition := schema.TableDef{
		Name: tableName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "city", Type: schema.TextType{}},
			{Name: "age", Type: schema.IntegerType{}},
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

	profileID := "postgresql-17"
	if test.dialect.Name() == "mysql" {
		profileID = "mysql-8.4"
	}
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, profileID)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	recordID := query.TypedColumnOf[record, int64](records.Column("id"))
	recordCity := query.TypedColumnOf[record, string](records.Column("city"))
	recordAge := query.TypedColumnOf[record, int64](records.Column("age"))
	for _, fixture := range []record{
		{ID: 1, City: "tokyo", Age: 30},
		{ID: 2, City: "osaka", Age: 20},
		{ID: 3, City: "tokyo", Age: 10},
	} {
		plan, err := rasql.NewCreatePlan(records,
			rasql.SetField(recordID, fixture.ID),
			rasql.SetField(recordCity, fixture.City),
			rasql.SetField(recordAge, fixture.Age),
		)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}

	table, err := query.NewTableRef(definition)
	require.NoError(t, err)
	city := table.Column("city")
	age := table.Column("age")

	t.Run("the server refuses ORDER BY on a column the distinct projections do not select", func(t *testing.T) {
		// query.Select places no Go-side rule here (see the WithDistinct doc
		// comment at query/select.go), so the builder renders this statement
		// and lets the server report its own error rather than refusing it
		// before rendering, the way it does for a misplaced aggregate.
		statement, err := query.NewSelect(table, city)
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(age))
		require.NoError(t, err)
		rendered, err := render.Select(test.dialect, statement)
		require.NoError(t, err, "rendering succeeds; the database is what refuses the statement")

		rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
		if err == nil {
			_ = rows.Close()
		}
		require.Error(t, err, "%s must refuse ORDER BY on a column outside the distinct projections", test.name)
	})

	t.Run("the database runs a distinct statement ordered by a projected column", func(t *testing.T) {
		statement, err := query.NewSelect(table, city)
		require.NoError(t, err)
		statement, err = statement.WithDistinct()
		require.NoError(t, err)
		statement, err = statement.WithOrder(query.Asc(city))
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
		require.Equal(t, []string{"osaka", "tokyo"}, cities)
	})
}

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

	profileID := "postgresql-17"
	if test.dialect.Name() == "mysql" {
		profileID = "mysql-8.4"
	}
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, profileID)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	recordID := query.TypedColumnOf[record, int64](records.Column("id"))
	recordCity := query.TypedColumnOf[record, string](records.Column("city"))
	for _, fixture := range []record{
		{ID: 1, City: "tokyo"},
		{ID: 2, City: "osaka"},
		{ID: 3, City: "tokyo"},
	} {
		plan, err := rasql.NewCreatePlan(records, rasql.SetField(recordID, fixture.ID), rasql.SetField(recordCity, fixture.City))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
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
