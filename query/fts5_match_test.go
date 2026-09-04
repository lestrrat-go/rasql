package query_test

import (
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// notesFTSTable describes a SQLite FTS5 virtual table with two indexed
// columns. The query and render packages never create this table — that is
// SQLite-specific DDL rasql.CreateTable already refuses (see
// docs/core/08-inspection-facts.md's "SQLite virtual tables" section) — so
// this descriptor exists only to build and validate statements against.
func notesFTSTable() schema.TableDef {
	return schema.TableDef{
		Name: "notes_fts",
		Columns: []schema.ColumnDef{
			{Name: "title", Type: schema.TextType{}},
			{Name: "body", Type: schema.TextType{}},
		},
	}
}

// TestMatchBuildsWholeTableComparison pins the shape query.Match builds: the
// table's own bare identifier on the left, OperatorMatch in the middle, and
// the query bound on the right exactly as Compare would bind it.
func TestMatchBuildsWholeTableComparison(t *testing.T) {
	notesFTS, err := query.NewTableRef(notesFTSTable())
	require.NoError(t, err)

	comparison := query.Match(notesFTS, "dinosaur")
	require.Equal(t, query.OperatorMatch, comparison.Operator())

	left, ok := comparison.Left().(query.TableIdentifier)
	require.True(t, ok, "Match's left operand is a TableIdentifier, got %T", comparison.Left())
	require.Equal(t, notesFTS, left.Table())

	right, ok := comparison.Right().(query.Value)
	require.True(t, ok, "Match binds a plain Go value on the right, got %T", comparison.Right())
	require.Equal(t, "dinosaur", right.Argument())
}

// TestCompareAcceptsMatchAgainstOneColumn proves the per-column MATCH shape
// Match itself does not build still works through the general-purpose
// Compare, exactly as query.Like does for LIKE.
func TestCompareAcceptsMatchAgainstOneColumn(t *testing.T) {
	notesFTS, err := query.NewTableRef(notesFTSTable())
	require.NoError(t, err)
	body := notesFTS.Column("body")

	comparison := query.Compare(body, query.OperatorMatch, "dinosaur")
	require.Equal(t, query.OperatorMatch, comparison.Operator())
	require.Equal(t, query.Expression(body), comparison.Left())

	statement, err := query.NewSelect(notesFTS, body)
	require.NoError(t, err)
	statement, err = statement.WithWhere(comparison)
	require.NoError(t, err)
	require.NotNil(t, statement.Where())
}

// TestSelectValidatesAStatementBuiltWithMatchAndBM25 proves the acceptance
// shape validates: a WHERE clause comparing the FTS5 table itself with MATCH,
// a projection scoring the match with BM25, and an ORDER BY repeating the
// same BM25 call, all against one FROM table.
func TestSelectValidatesAStatementBuiltWithMatchAndBM25(t *testing.T) {
	notesFTS, err := query.NewTableRef(notesFTSTable())
	require.NoError(t, err)

	score := query.BM25(notesFTS, 2.0, 1.0)
	statement, err := query.NewSelect(notesFTS, notesFTS.Column("title"), score.As("score"))
	require.NoError(t, err)

	statement, err = statement.WithWhere(query.Match(notesFTS, "dinosaur"))
	require.NoError(t, err)

	statement, err = statement.WithOrder(query.Asc(score))
	require.NoError(t, err)

	statement, err = statement.WithLimit(10)
	require.NoError(t, err)
	require.NotNil(t, statement.Where())
}

// TestTableIdentifierRefusesATableOutsideTheStatement proves TableIdentifier
// follows the same "outside the statement" rule ColumnRef follows: a table
// this statement never selects from is refused rather than silently
// rendered.
func TestTableIdentifierRefusesATableOutsideTheStatement(t *testing.T) {
	notesFTS, err := query.NewTableRef(notesFTSTable())
	require.NoError(t, err)
	other, err := query.NewTableRef(schema.TableDef{
		Name:    "other_fts",
		Columns: []schema.ColumnDef{{Name: "body", Type: schema.TextType{}}},
	})
	require.NoError(t, err)

	_, err = query.NewSelect(notesFTS, notesFTS.Column("title"), query.Project(other.Identifier()))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, `references table "other_fts" outside the statement`)
}

// TestBM25RequiresATableIdentifierFirstArgument proves the one shape check
// FunctionBM25 adds beyond a plain arity check: SQL itself cannot tell a
// caller they passed the wrong kind of first argument, since a ColumnRef or a
// bound value both look like plausible arguments once rendered, so
// validation checks the Go type instead.
func TestBM25RequiresATableIdentifierFirstArgument(t *testing.T) {
	notesFTS, err := query.NewTableRef(notesFTSTable())
	require.NoError(t, err)

	wrongShape := query.Call(query.FunctionBM25, notesFTS.Column("title"), 1.0)
	_, err = query.NewSelect(notesFTS, wrongShape.As("score"))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, fmt.Sprintf("function %q takes a TableIdentifier as its first argument", query.FunctionBM25))

	noArguments := query.Call(query.FunctionBM25)
	_, err = query.NewSelect(notesFTS, noArguments.As("score"))
	requireQueryValidationError(t, err)
	require.ErrorContains(t, err, fmt.Sprintf("function %q takes the table's own identifier as its first argument", query.FunctionBM25))
}

// TestBM25AcceptsNoWeightsAtAll proves the arity is variable, unlike the
// fixed-arity curated functions: SQLite defaults every column's weight to
// 1.0 when none are given.
func TestBM25AcceptsNoWeightsAtAll(t *testing.T) {
	notesFTS, err := query.NewTableRef(notesFTSTable())
	require.NoError(t, err)

	score := query.BM25(notesFTS)
	require.Len(t, score.Arguments(), 1)
	_, err = query.NewSelect(notesFTS, score.As("score"))
	require.NoError(t, err)
}
