package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// BEGIN(render_select_match)

func Example_query_render_select_match() {
	// query.TableRef carries no Go row type here, exactly as in
	// query_render_select_example_test.go, so every column is still named
	// by string rather than through a generated accessor.
	notesFTS := query.MustTableRef(schema.TableDef{
		Name: "notes_fts",
		Columns: []schema.ColumnDef{
			{Name: "title", Type: schema.TextType{}},
			{Name: "body", Type: schema.TextType{}},
		},
	})

	// query.BM25 takes the table's own identifier as its first argument,
	// never a plain string, so "notes_fts" reaches SQL through
	// Dialect.QuoteIdentifier rather than as a bound parameter. The two
	// weights that follow favor a match in title over one in body.
	score := query.BM25(notesFTS, 2.0, 1.0)
	statement, err := query.NewSelect(notesFTS, notesFTS.Column("title"), score.As("score"))
	if err != nil {
		fmt.Printf("failed to build the select: %s\n", err)
		return
	}

	// query.Match builds the same shape for MATCH: the table's own
	// identifier on the left, so it filters every indexed column at once.
	statement, err = statement.WithWhere(query.Match(notesFTS, "dinosaur"))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}
	// rasql orders by an expression, not by a projection's result name, so
	// the ordering repeats the same BM25 call rather than naming "score".
	statement, err = statement.WithOrder(query.Asc(score))
	if err != nil {
		fmt.Printf("failed to add the ordering: %s\n", err)
		return
	}

	// MATCH is SQLite-only: render.Select refuses it for a dialect that
	// lacks dialect.CapabilityMatchOperator rather than send SQL neither
	// PostgreSQL nor MySQL understands.
	rendered, err := render.Select(dialect.SQLite(), statement)
	if err != nil {
		fmt.Printf("failed to render the select: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	if _, err := render.Select(dialect.PostgreSQL(), statement); err != nil {
		fmt.Println(err)
	}

	// Output:
	// SELECT "notes_fts"."title", BM25("notes_fts", ?, ?) AS "score" FROM "notes_fts" WHERE ("notes_fts" MATCH ?) ORDER BY BM25("notes_fts", ?, ?)
	// 2 1 dinosaur 2 1
	// render postgresql: the postgresql dialect cannot express MATCH: it has no full-text search operator
}

// END(render_select_match)
