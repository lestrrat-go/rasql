package render_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// notesFTSStatement builds the SELECT the motivating mecp query stands in
// for: a search against a virtual table's own bare identifier with MATCH, a
// BM25 score projected under an alias, and an ORDER BY repeating the same
// BM25 call, since rasql's ORDER BY takes an expression rather than a
// projection's result name.
func notesFTSStatement(t *testing.T) query.Select {
	t.Helper()
	notesFTS := query.MustTableRef(schema.TableDef{
		Name: "notes_fts",
		Columns: []schema.ColumnDef{
			{Name: "title", Type: schema.TextType{}},
			{Name: "body", Type: schema.TextType{}},
		},
	})
	score := query.BM25(notesFTS, 2.0, 1.0)
	statement, err := query.NewSelect(notesFTS, notesFTS.Column("title"), score.As("score"))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Match(notesFTS, "dinosaur"))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Asc(score))
	require.NoError(t, err)
	return statement
}

// TestSelectRendersMatchAndBM25ForSQLite pins the exact SQL SQLite renders
// for the two gaps together: TableIdentifier renders records_fts as a bare
// quoted identifier, both as MATCH's left operand and as BM25's first
// argument, and every weight and query string still travels as a bound
// placeholder rather than as SQL text.
func TestSelectRendersMatchAndBM25ForSQLite(t *testing.T) {
	statement := notesFTSStatement(t)

	rendered, err := render.Select(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t,
		`SELECT "notes_fts"."title", BM25("notes_fts", ?, ?) AS "score" FROM "notes_fts" WHERE ("notes_fts" MATCH ?) ORDER BY BM25("notes_fts", ?, ?)`,
		rendered.SQL(),
	)
	require.Equal(t, []any{2.0, 1.0, "dinosaur", 2.0, 1.0}, rendered.Args())
	require.NotContains(t, rendered.SQL(), "dinosaur")
}

// TestSelectRefusesMatchOnPostgreSQLAndMySQL proves render.Select refuses to
// send MATCH to a dialect that cannot run it rather than rendering SQL the
// server would reject, for both dialects that lack
// dialect.CapabilityMatchOperator.
func TestSelectRefusesMatchOnPostgreSQLAndMySQL(t *testing.T) {
	statement := notesFTSStatement(t)

	tests := map[string]dialect.Dialect{
		"postgresql": dialect.PostgreSQL(),
		"mysql":      dialect.MySQL(),
	}
	for name, d := range tests {
		t.Run(name, func(t *testing.T) {
			require.False(t, d.Supports(dialect.CapabilityMatchOperator))

			rendered, err := render.Select(d, statement)
			require.Empty(t, rendered.SQL(), "a refused statement renders no SQL")

			var matchErr *render.UnsupportedMatchOperatorError
			require.True(t, errors.As(err, &matchErr))
			require.Equal(t, d.Name(), matchErr.Dialect)
			require.True(t, errors.Is(err, render.ErrUnsupportedMatchOperator))
			require.ErrorContains(t, err, "cannot express MATCH")
		})
	}
}
