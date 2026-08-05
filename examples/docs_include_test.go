package examples_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// updateDocs rewrites every included block instead of comparing it, so the
// documentation never holds a hand-copied example.
var updateDocs = flag.Bool("update-docs", false, "rewrite included example blocks in README.md and docs/")

// repositoryRoot is the module root as seen from this package's directory.
const repositoryRoot = ".."

// sourceURLPrefix links each included block back to the file it was copied from.
const sourceURLPrefix = "https://github.com/lestrrat-go/rasql/blob/main/"

// includeBlock matches one delimited block and captures the example path it
// includes together with the body that must match that file.
var includeBlock = regexp.MustCompile(`(?s)<!-- INCLUDE\((examples/[^)]+)\) -->\n(.*?)<!-- END INCLUDE -->`)

func TestDocExamplesMatchSource(t *testing.T) {
	blocks := 0
	for _, page := range documentationPages(t) {
		contents, err := os.ReadFile(page)
		require.NoError(t, err)

		text := string(contents)
		rewritten := includeBlock.ReplaceAllStringFunc(text, func(block string) string {
			match := includeBlock.FindStringSubmatch(block)
			blocks++

			source, err := os.ReadFile(filepath.Join(repositoryRoot, match[1]))
			require.NoError(t, err)
			body := renderInclude(match[1], strings.TrimSuffix(string(source), "\n"))
			if !*updateDocs {
				require.Equal(t, body, match[2], "%s includes a stale copy of %s; run `go test ./examples/ -update-docs`", page, match[1])
			}
			return fmt.Sprintf("<!-- INCLUDE(%s) -->\n%s<!-- END INCLUDE -->", match[1], body)
		})
		if *updateDocs && rewritten != text {
			require.NoError(t, os.WriteFile(page, []byte(rewritten), 0o644))
			t.Logf("updated %s", page)
		}
	}
	require.NotZero(t, blocks, "no example blocks found in the documentation")
}

// execMethod matches the Client.Exec method by name. The word boundary keeps
// ExecRendered and a builder's own Exec out, since neither rejects RETURNING.
var execMethod = regexp.MustCompile(`\b[Cc]lient\.Exec\b`)

// returningClause matches the SQL keyword naming the clause Client.Exec refuses
// to run.
var returningClause = regexp.MustCompile(`\bRETURNING\b`)

// returningRoute matches the calls that do read those rows back: the untyped
// Client.QueryWrite and the typed rasql.QueryWriteAll and rasql.QueryWriteOne.
var returningRoute = regexp.MustCompile(`\bQueryWrite(?:All|One)?\b`)

// execRefusal matches the wording that denies Client.Exec a RETURNING write. A
// sentence may name both as long as it says the combination does not run.
var execRefusal = regexp.MustCompile(`(?i)\b(?:rejects?|rejected|refuses?|cannot|can't|does not|doesn't|except|instead|no|not)\b`)

// passageUnit splits a passage into the pieces a reader follows one at a time:
// one sentence per period, and one row per line, since a table row carries its
// own routing advice. Documentation here keeps each paragraph on one line.
var passageUnit = regexp.MustCompile(`\.\s+|\n`)

// fencedBlock matches a fenced code block, which holds example source rather
// than the prose that routes a reader to an API.
var fencedBlock = regexp.MustCompile("(?s)```.*?```")

// execPassageProblem reports why a passage breaks the rule Client.Exec
// enforces, or the empty string when the passage keeps it. A passage that never
// names Client.Exec is not governed by the rule and is always kept.
//
// A governed passage has to do three things: name the RETURNING clause that
// Exec rejects, name the QueryWrite call that reads those rows back, and avoid
// sending a RETURNING write to Exec in the sentence or table row a reader acts
// on. The last check is what stops a passage from earning its keep on a
// RETURNING mention that routes the caller into a failing Exec call anyway.
func execPassageProblem(passage string) string {
	if !execMethod.MatchString(passage) {
		return ""
	}
	if !returningClause.MatchString(passage) {
		return "names Client.Exec without stating that it rejects a write carrying a RETURNING clause"
	}
	if !returningRoute.MatchString(passage) {
		return "states the RETURNING exception without naming QueryWrite, QueryWriteAll, or QueryWriteOne as the call that reads those rows"
	}
	for _, unit := range passageUnit.Split(passage, -1) {
		if !execMethod.MatchString(unit) || !returningClause.MatchString(unit) {
			continue
		}
		if returningRoute.MatchString(unit) || execRefusal.MatchString(unit) {
			continue
		}
		return fmt.Sprintf("sends a RETURNING write to Client.Exec in %q", strings.TrimSpace(unit))
	}
	return ""
}

// execPassageFixtures pin execPassageProblem to concrete prose. The entries
// marked historical are the wording this pull request replaced, quoted verbatim
// so the guard is proven against the exact text it exists to reject; every
// other entry is invented for this table and appears nowhere in the tree.
var execPassageFixtures = []struct {
	name    string
	passage string
	reject  bool
}{
	{
		name:    "historical README overview routes RETURNING to Exec",
		passage: "Inserts, updates, deletes, and typed selects have dedicated helpers. Upserts, `RETURNING`, and anything else beyond them are built through the `query` package and run with `client.Exec`.",
		reject:  true,
	},
	{
		name:    "historical writing page offers Exec for a RETURNING clause",
		passage: "`Client.Exec` runs any `query.WriteStatement`, which is what the `query` constructors produce: `NewInsert`, `NewUpdate`, `NewDelete`, and `NewUpsert`. Use them for a partial update, conflict handling, or a `RETURNING` clause.",
		reject:  true,
	},
	{
		name:    "historical builder note claims Exec runs any statement",
		passage: "The builders cover the common statements. These constructors build the same statements directly, and `client.Exec` runs any of them.",
		reject:  true,
	},
	{
		name:    "historical writing intro sends everything uncovered to Exec",
		passage: "The root package writes a typed row without building a statement by hand. For anything the typed helpers do not cover, the `query` package builds the statement and `Client.Exec` runs it.",
		reject:  true,
	},
	{
		name: "historical operation table lists RETURNING under Exec",
		passage: "| Operation | Entry point | Result |\n" +
			"| --- | --- | --- |\n" +
			"| Upsert, `RETURNING`, partial update | `query.New…` then `client.Exec(ctx, statement)` | `sql.Result` |",
		reject: true,
	},
	{
		name: "invented table keeps a RETURNING row beside an Exec row that claims it",
		passage: "| Upsert, `RETURNING`, partial update | `query.New…` then `client.Exec(ctx, statement)` | `sql.Result` |\n" +
			"| Write with `RETURNING` | `client.QueryWrite(ctx, statement)` | `row.Row` |",
		reject: true,
	},
	{
		name:    "invented prose names QueryWrite elsewhere while offering Exec a RETURNING write",
		passage: "Build the upsert with the `query` package and run it with `client.Exec`, including a `RETURNING` clause. `client.QueryWrite` runs a select-shaped write.",
		reject:  true,
	},
	{
		name:    "invented prose states the rejection and the replacement call",
		passage: "The `query` package builds the statement and `client.Exec` runs it, unless the statement carries a `RETURNING` clause; `client.QueryWrite` reads those rows back instead.",
		reject:  false,
	},
	{
		name:    "invented prose names a typed QueryWrite helper",
		passage: "`Client.Exec` rejects a write carrying a `RETURNING` clause, so decode those rows with `rasql.QueryWriteOne[T]`.",
		reject:  false,
	},
	{
		name: "invented table routes RETURNING away from Exec",
		passage: "| Upsert, partial update | `query.New…` then `client.Exec(ctx, statement)` | `sql.Result` |\n" +
			"| Write with `RETURNING` | `client.QueryWrite(ctx, statement)` | `row.Row` |",
		reject: false,
	},
	{
		name:    "invented passage names ExecRendered only",
		passage: "`client.ExecRendered` runs a statement that is already rendered, which is how a compiled static template is executed.",
		reject:  false,
	},
}

// TestDocsQualifyExecWithReturningRule holds the documentation to the rule
// Client.Exec enforces: it rejects a write statement carrying a RETURNING
// clause, so prose that sends a caller to Exec must name that exception and the
// QueryWrite call that reads those rows, rather than route the caller into a
// failing call.
func TestDocsQualifyExecWithReturningRule(t *testing.T) {
	t.Run("fixtures", func(t *testing.T) {
		for _, fixture := range execPassageFixtures {
			t.Run(fixture.name, func(t *testing.T) {
				problem := execPassageProblem(fixture.passage)
				if fixture.reject {
					require.NotEmpty(t, problem, "the rule accepts a passage that routes a RETURNING write to Client.Exec:\n%s", fixture.passage)
					return
				}
				require.Empty(t, problem, "the rule rejects a passage that already qualifies Client.Exec: %s", problem)
			})
		}
	})

	t.Run("documentation", func(t *testing.T) {
		passages := 0
		for _, page := range documentationPages(t) {
			contents, err := os.ReadFile(page)
			require.NoError(t, err)

			prose := fencedBlock.ReplaceAllString(string(contents), "")
			for _, paragraph := range strings.Split(prose, "\n\n") {
				if !execMethod.MatchString(paragraph) {
					continue
				}
				passages++
				problem := execPassageProblem(paragraph)
				require.Empty(t, problem,
					"%s %s; say which call reads those rows instead:\n%s",
					page, problem, strings.TrimSpace(paragraph))
			}
		}
		require.NotZero(t, passages, "no Client.Exec passage found in the documentation")
	})
}

// documentationPages lists every markdown file that may include an example.
func documentationPages(t *testing.T) []string {
	t.Helper()

	pages, err := filepath.Glob(filepath.Join(repositoryRoot, "docs", "*.md"))
	require.NoError(t, err)
	return append([]string{filepath.Join(repositoryRoot, "README.md")}, pages...)
}

// renderInclude creates the body of an include block: the example source as a
// Go code fence, followed by a link to the file it came from.
func renderInclude(source, contents string) string {
	return fmt.Sprintf("```go\n%s\n```\nsource: [%s](%s%s)\n", contents, source, sourceURLPrefix, source)
}
