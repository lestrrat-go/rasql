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

// TestSQLiteRunsMatchAndBM25AgainstALiveDatabase proves the two rasql gaps
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
func TestSQLiteRunsMatchAndBM25AgainstALiveDatabase(t *testing.T) {
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
