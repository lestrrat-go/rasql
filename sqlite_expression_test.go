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

func TestSQLiteComposableExpressionsInTransaction(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO accounts (id, balance) VALUES (1, 10), (2, 20)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(tx, profile)
	require.NoError(t, err)

	table := query.MustTableRef(schema.MustTableDef("accounts", schema.Integer("id"), schema.Integer("balance")))
	id, balance := table.Column("id"), table.Column("balance")
	update, err := query.NewUpdate(table, query.Set(balance, query.Add(balance, 1)))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, 1))
	require.NoError(t, err)
	require.NoError(t, executeUpdate(t, executor, update))

	label := query.SearchedCase(query.When(query.GreaterThan(balance, 10), "large")).Else("small")
	fragment := query.TrustedSQL("{} + {} + {}", query.IdentifierHole(query.Ident("balance")), query.Hole(4), query.Hole(5))
	selectStatement, err := query.NewSelect(table,
		id,
		query.Project(label).As("label"),
		query.Project(query.CastAs(balance, schema.IntegerType{})).As("cast_balance"),
		query.Project(fragment).As("fragment_total"),
	)
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), selectStatement)
	require.NoError(t, err)
	require.Equal(t, []any{10, "large", "small", 4, 5}, rendered.Args())
	rows, err := tx.QueryRendered(t.Context(), rendered)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	type result struct {
		id            int64
		label         string
		castBalance   int64
		fragmentTotal int64
	}
	var results []result
	for rows.Next() {
		var current result
		require.NoError(t, rows.Scan(&current.id, &current.label, &current.castBalance, &current.fragmentTotal))
		results = append(results, current)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []result{{1, "large", 11, 20}, {2, "large", 20, 29}}, results)

	windowStatement, err := query.NewSelect(table,
		query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(id)))).As("row_number"))
	require.NoError(t, err)
	windowRendered, err := render.Select(dialect.SQLite(), windowStatement)
	require.NoError(t, err)
	windowRows, err := tx.QueryRendered(t.Context(), windowRendered)
	require.NoError(t, err)
	defer func() { _ = windowRows.Close() }()
	var windowResults []int64
	for windowRows.Next() {
		var rowNumber int64
		require.NoError(t, windowRows.Scan(&rowNumber))
		windowResults = append(windowResults, rowNumber)
	}
	require.NoError(t, windowRows.Err())
	require.Equal(t, []int64{1, 2}, windowResults)
	require.NoError(t, tx.Commit())

	var stored int64
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT balance FROM accounts WHERE id = 1`).Scan(&stored))
	require.Equal(t, int64(11), stored)
}

func executeUpdate(t *testing.T, executor rasql.Executor, statement query.Update) error {
	t.Helper()
	return rasqlExec(t, executor, statement)
}

func rasqlExec(t *testing.T, executor rasql.Executor, statement query.WriteStatement) error {
	t.Helper()
	plan, err := rasql.NewStatementPlan(statement)
	if err != nil {
		return err
	}
	_, err = rasql.ExecMutation(t.Context(), executor, plan)
	return err
}

// TestSQLiteRunsMatchAndBM25 proves the two rasql gaps
// this change closes — MATCH and a table's own bare identifier in expression
// position — against a real SQLite database, not a fixture asserting rasql's
// own output back to itself. It is the shape CLAUDE.md's "Verifying live
// database behavior" section asks for: a real engine actually answers the
// statement, and the result rows are checked against what full-text search
// should find, not against a pinned SQL string.
//
// render.CreateTable cannot build the FTS5 virtual table itself — SQLite
// virtual tables are explicitly out of scope for DDL rendering, per
// docs/core/08-inspection-facts.md's "SQLite virtual tables" section — so
// the fixture creates it with a raw CREATE VIRTUAL TABLE statement, exactly
// as an application using rasql alongside FTS5 has to.
func TestSQLiteRunsMatchAndBM25(t *testing.T) {
	database, records, recordsFTS := fts5MatchFixture(t)

	// The statement mirrors the motivating query in the package documentation:
	// a JOIN back to the content table, a MATCH against the FTS5 table's own
	// identifier, an extra predicate on a joined column, a BM25 score
	// projected under an alias, and an ORDER BY that repeats the same BM25
	// call — rasql orders by an expression, not by a projection's alias.
	category := records.Column("category")
	id := records.Column("id")
	title := records.Column("title")
	score := query.BM25(recordsFTS, 2.0, 1.0)

	statement, err := query.NewJoinedSelect(recordsFTS,
		[]query.Join{query.InnerJoin(records, query.Equal(id, recordsFTS.Column("rowid")))},
		nil,
		id, title, score.As("score"),
	)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.And(
		query.Match(recordsFTS, "dinosaur"),
		query.Equal(category, "article"),
	))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Asc(score), query.Asc(id))
	require.NoError(t, err)
	statement, err = statement.WithLimit(10)
	require.NoError(t, err)

	rendered, err := render.Select(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "records"."id", "records"."title", BM25("notes_fts", ?, ?) AS "score" `+
			`FROM "notes_fts" INNER JOIN "records" ON ("records"."id" = "notes_fts"."rowid") `+
			`WHERE (("notes_fts" MATCH ?) AND ("records"."category" = ?)) `+
			`ORDER BY BM25("notes_fts", ?, ?), "records"."id" LIMIT ?`,
		rendered.SQL(),
	)
	require.NotContains(t, rendered.SQL(), "dinosaur", "the query string travels as an argument, never as SQL text")

	rows, err := database.QueryContext(t.Context(), rendered.SQL(), rendered.Args()...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	type result struct {
		id    int64
		title string
		score float64
	}
	var results []result
	for rows.Next() {
		var r result
		require.NoError(t, rows.Scan(&r.id, &r.title, &r.score))
		results = append(results, r)
	}
	require.NoError(t, rows.Err())

	// Records 2 (recipe) and 4 (no match) are excluded by the MATCH and the
	// category predicate; only 1 and 3 remain, both category "article" and
	// both matching "dinosaur".
	require.Len(t, results, 2)
	var ids []int64
	for i, r := range results {
		ids = append(ids, r.id)
		if i > 0 {
			require.LessOrEqualf(t, results[i-1].score, r.score, "ORDER BY score must leave the best (lowest bm25) match first")
		}
	}
	require.ElementsMatch(t, []int64{1, 3}, ids)
	// Record 3 repeats "dinosaur" twice across title and body against
	// record 1's single mention, so bm25 ranks it the better match — the
	// lower score sorts first.
	require.Equal(t, []int64{3, 1}, ids)
}

// fts5MatchFixture opens an in-memory SQLite database holding a content table
// and an FTS5 virtual table over it, joined by rowid, and returns both table
// descriptions alongside the open database.
func fts5MatchFixture(t *testing.T) (*sql.DB, query.TableRef, query.TableRef) {
	t.Helper()

	recordsDefinition := schema.TableDef{
		Name: "records",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
			{Name: "body", Type: schema.TextType{}},
			{Name: "category", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}
	// notes_fts describes SQLite's own hidden rowid column too, so the
	// statement above can join on it: FTS5 never declares it explicitly, but
	// every SQLite table, virtual or not, answers to it.
	recordsFTSDefinition := schema.TableDef{
		Name: "notes_fts",
		Columns: []schema.ColumnDef{
			{Name: "title", Type: schema.TextType{}},
			{Name: "body", Type: schema.TextType{}},
			{Name: "rowid", Type: schema.IntegerType{}},
		},
	}

	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	// An in-memory SQLite database is per connection, so keep the test on one.
	database.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err = database.ExecContext(ctx, `CREATE TABLE records (id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, category TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `CREATE VIRTUAL TABLE notes_fts USING fts5(title, body)`)
	require.NoError(t, err)

	type record struct {
		id       int64
		title    string
		body     string
		category string
	}
	fixtures := []record{
		{id: 1, title: "Dinosaur bones discovered", body: "A team found dinosaur bones in the desert.", category: "article"},
		{id: 2, title: "Cooking with tomatoes", body: "A recipe about tomatoes and a dinosaur-shaped pasta cutter.", category: "recipe"},
		{id: 3, title: "Ancient reptiles, dinosaur edition", body: "Dinosaur fossils reveal ancient reptile secrets.", category: "article"},
		{id: 4, title: "Weather report", body: "Sunny with a chance of rain.", category: "article"},
	}
	for _, f := range fixtures {
		_, err = database.ExecContext(ctx, `INSERT INTO records (id, title, body, category) VALUES (?, ?, ?, ?)`, f.id, f.title, f.body, f.category)
		require.NoError(t, err)
		_, err = database.ExecContext(ctx, `INSERT INTO notes_fts (rowid, title, body) VALUES (?, ?, ?)`, f.id, f.title, f.body)
		require.NoError(t, err)
	}

	records, err := query.NewTableRef(recordsDefinition)
	require.NoError(t, err)
	recordsFTS, err := query.NewTableRef(recordsFTSDefinition)
	require.NoError(t, err)
	return database, records, recordsFTS
}

func TestSQLiteConditionalUpsert(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, payload TEXT NOT NULL)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table := query.MustTableRef(schema.MustTableDef("items", schema.Integer("id"), schema.Integer("version"), schema.Text("payload")))
	id, version, payload := table.Column("id"), table.Column("version"), table.Column("payload")
	seed, err := query.NewInsert(table, query.Set(id, 1), query.Set(version, 2), query.Set(payload, "v2"))
	require.NoError(t, err)
	seedPlan, err := rasql.NewStatementPlan(seed)
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, seedPlan)
	require.NoError(t, err)

	upsert := func(versionValue int, payloadValue string) {
		insert, buildErr := query.NewInsert(table, query.Set(id, 1), query.Set(version, versionValue), query.Set(payload, payloadValue))
		require.NoError(t, buildErr)
		statement, buildErr := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{
			query.Set(version, query.Excluded(version)), query.Set(payload, query.Excluded(payload)),
		})
		require.NoError(t, buildErr)
		statement, buildErr = statement.WithUpdateWhere(query.LessThan(version, query.Excluded(version)))
		require.NoError(t, buildErr)
		statement, buildErr = statement.WithConflictWhere(query.GreaterThan(version, 0))
		require.NoError(t, buildErr)
		plan, planErr := rasql.NewStatementPlan(statement)
		require.NoError(t, planErr)
		_, execErr := rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, execErr)
	}
	upsert(1, "v1")
	var storedVersion int
	var storedPayload string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT version, payload FROM items WHERE id = 1`).Scan(&storedVersion, &storedPayload))
	require.Equal(t, 2, storedVersion)
	require.Equal(t, "v2", storedPayload)
	upsert(3, "v3")
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT version, payload FROM items WHERE id = 1`).Scan(&storedVersion, &storedPayload))
	require.Equal(t, 3, storedVersion)
	require.Equal(t, "v3", storedPayload)
}
