package examples_test

import (
	"flag"
	"fmt"
	"io/fs"
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

// includeBlock matches one delimited block and captures the example target it
// includes together with the body that must match that target.
var includeBlock = regexp.MustCompile(`(?s)<!-- INCLUDE\(((?:examples|sample)/[^)]+)\) -->\n(.*?)<!-- END INCLUDE -->`)

// regionMarker matches one region delimiter in an example file. A region is
// named so a page can include the few lines that make its point instead of a
// whole example, which is what keeps a short passage short without letting it
// hold code no compiler ever sees.
var regionMarker = regexp.MustCompile(`^[\t ]*// (BEGIN|END)\(([A-Za-z0-9_]+)\)[\t ]*$`)

func TestDocExamplesMatchSource(t *testing.T) {
	blocks := 0
	for _, page := range documentationPages(t) {
		contents, err := os.ReadFile(page)
		require.NoError(t, err)

		text := string(contents)
		rewritten := includeBlock.ReplaceAllStringFunc(text, func(block string) string {
			match := includeBlock.FindStringSubmatch(block)
			blocks++

			included, err := includedSource(match[1])
			require.NoError(t, err, "%s includes %s", page, match[1])
			body := renderInclude(match[1], included)
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

// goFence matches one fenced Go block, which is the only kind of block that
// has to come from a compiled example.
var goFence = regexp.MustCompile("(?m)^```go$")

// TestDocGoBlocksComeFromExamples holds every Go block in the documentation to
// a file the compiler and `go test` see. A snippet written straight into a page
// answers to nothing and drifts from the API it describes, so the fence has to
// sit inside an include block rather than be maintained by hand.
func TestDocGoBlocksComeFromExamples(t *testing.T) {
	for _, page := range documentationPages(t) {
		contents, err := os.ReadFile(page)
		require.NoError(t, err)

		text := string(contents)
		included := includeBlock.FindAllStringIndex(text, -1)
		for _, fence := range goFence.FindAllStringIndex(text, -1) {
			within := false
			for _, block := range included {
				if fence[0] > block[0] && fence[0] < block[1] {
					within = true
					break
				}
			}
			require.True(t, within,
				"%s holds a hand-written Go block at line %d; move the code into an example under examples/ and include it with <!-- INCLUDE(examples/…) -->",
				page, strings.Count(text[:fence[0]], "\n")+1)
		}
	}
}

// TestDocRegionsAreIncluded fails on a region no page includes. A marker left
// behind after a passage is rewritten reads as documented source while nothing
// shows it, so the example keeps only the regions the documentation uses.
func TestDocRegionsAreIncluded(t *testing.T) {
	referenced := map[string]struct{}{}
	for _, page := range documentationPages(t) {
		contents, err := os.ReadFile(page)
		require.NoError(t, err)
		for _, match := range includeBlock.FindAllStringSubmatch(string(contents), -1) {
			referenced[match[1]] = struct{}{}
		}
	}

	declared := 0
	for _, source := range exampleSources(t) {
		contents, err := os.ReadFile(filepath.Join(repositoryRoot, source))
		require.NoError(t, err)
		for _, line := range strings.Split(string(contents), "\n") {
			match := regionMarker.FindStringSubmatch(line)
			if match == nil || match[1] != "BEGIN" {
				continue
			}
			declared++
			target := source + "#" + match[2]
			_, used := referenced[target]
			require.True(t, used, "%s declares region %q that no page includes; delete the markers or include %s", source, match[2], target)
		}
	}
	require.NotZero(t, declared, "no example declares a region")
}

// regionFixtures pin what a region include extracts. Every source here is
// invented for this table so the fixtures keep their meaning when the examples
// they resemble are rewritten.
var regionFixtures = []struct {
	name    string
	source  string
	region  string
	extract string
	problem string
}{
	{
		name:    "region inside a function is shifted to the left margin",
		source:  "func Example() {\n\t// BEGIN(open)\n\tdb, err := rasql.New(database, dialect.SQLite())\n\tif err != nil {\n\t\treturn\n\t}\n\t// END(open)\n}\n",
		region:  "open",
		extract: "db, err := rasql.New(database, dialect.SQLite())\nif err != nil {\n\treturn\n}",
	},
	{
		name:    "a blank line inside a region survives the shift",
		source:  "func Example() {\n\t// BEGIN(pair)\n\tfirst := 1\n\n\tsecond := 2\n\t// END(pair)\n}\n",
		region:  "pair",
		extract: "first := 1\n\nsecond := 2",
	},
	{
		name:    "a marker for another region is dropped from the body",
		source:  "func Example() {\n\t// BEGIN(outer)\n\tfirst := 1\n\t// BEGIN(inner)\n\tsecond := 2\n\t// END(inner)\n\t// END(outer)\n}\n",
		region:  "outer",
		extract: "first := 1\nsecond := 2",
	},
	{
		name:    "a top-level region keeps its own indentation",
		source:  "// BEGIN(row)\ntype UserRow struct {\n\tID int64\n}\n// END(row)\n",
		region:  "row",
		extract: "type UserRow struct {\n\tID int64\n}",
	},
	{
		name:    "a region the file never declares is reported",
		source:  "func Example() {\n\t// BEGIN(open)\n\tfirst := 1\n\t// END(open)\n}\n",
		region:  "missing",
		problem: "declares no region",
	},
	{
		name:    "a region opened twice is reported",
		source:  "// BEGIN(open)\nfirst := 1\n// BEGIN(open)\nsecond := 2\n// END(open)\n",
		region:  "open",
		problem: "opens region",
	},
	{
		name:    "a region closed without opening it is reported",
		source:  "first := 1\n// END(open)\n",
		region:  "open",
		problem: "without opening it",
	},
	{
		name:    "a region left open is reported",
		source:  "// BEGIN(open)\nfirst := 1\n",
		region:  "open",
		problem: "never closes it",
	},
	{
		name:    "a region holding no source is reported",
		source:  "// BEGIN(open)\n// END(open)\n",
		region:  "open",
		problem: "holds no source",
	},
	{
		name:    "the blank line gofmt leaves before the closing marker is dropped",
		source:  "// BEGIN(row)\ntype UserRow struct {\n\tID int64\n}\n\n// END(row)\n",
		region:  "row",
		extract: "type UserRow struct {\n\tID int64\n}",
	},
	{
		name:    "a region holding only blank lines is reported",
		source:  "// BEGIN(open)\n\n\n// END(open)\n",
		region:  "open",
		problem: "holds no source",
	},
}

func TestRegionExtraction(t *testing.T) {
	for _, fixture := range regionFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			lines := strings.Split(strings.TrimSuffix(fixture.source, "\n"), "\n")
			extract, err := regionSource("example_test.go", fixture.region, lines)
			if fixture.problem != "" {
				require.ErrorContains(t, err, fixture.problem)
				return
			}
			require.NoError(t, err)
			require.Equal(t, fixture.extract, extract)
		})
	}
}

// exampleSources lists every Go file an include target may name, as a
// repository-relative path. The sample application is searched too, since a
// page showing how a project wires rasqlgen in takes that source from the one
// project in this repository that does.
func exampleSources(t *testing.T) []string {
	t.Helper()

	var sources []string
	for _, root := range []string{"examples", "sample"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(repositoryRoot, root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			sources = append(sources, filepath.ToSlash(relative))
			return nil
		}))
	}
	return sources
}

// includeTarget splits an include target into the example file it names and
// the region within that file, which is empty when the target includes the
// whole file.
func includeTarget(target string) (string, string) {
	file, region, found := strings.Cut(target, "#")
	if !found {
		return target, ""
	}
	return file, region
}

// includedSource returns the source a target contributes to a page: the whole
// example file, or the one region it names. Either way the region markers
// themselves are dropped, since they delimit source for this test rather than
// showing a reader anything.
func includedSource(target string) (string, error) {
	file, region := includeTarget(target)
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, file))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if region == "" {
		return strings.Join(withoutMarkers(lines), "\n"), nil
	}
	return regionSource(file, region, lines)
}

// withoutMarkers drops every region marker line.
func withoutMarkers(lines []string) []string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if regionMarker.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// regionSource returns the lines one region delimits, shifted left by the
// indentation they all share so a region taken from inside a function reads as
// a top-level snippet.
func regionSource(file, region string, lines []string) (string, error) {
	start := -1
	for number, line := range lines {
		match := regionMarker.FindStringSubmatch(line)
		if match == nil || match[2] != region {
			continue
		}
		if match[1] == "BEGIN" {
			if start >= 0 {
				return "", fmt.Errorf("%s opens region %q again at line %d", file, region, number+1)
			}
			start = number
			continue
		}
		if start < 0 {
			return "", fmt.Errorf("%s closes region %q at line %d without opening it", file, region, number+1)
		}
		body := trimBlankLines(withoutMarkers(lines[start+1 : number]))
		if len(body) == 0 {
			return "", fmt.Errorf("%s holds no source in region %q", file, region)
		}
		return strings.Join(dedent(body), "\n"), nil
	}
	if start >= 0 {
		return "", fmt.Errorf("%s opens region %q at line %d and never closes it", file, region, start+1)
	}
	return "", fmt.Errorf("%s declares no region %q", file, region)
}

// trimBlankLines drops the blank lines at either end of a region body. gofmt
// puts one before the comment that closes a region declared around a top-level
// declaration, and that blank line belongs to the file rather than to the
// passage including it.
func trimBlankLines(lines []string) []string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[start:end]
}

// dedent removes the longest whitespace prefix every non-blank line shares.
func dedent(lines []string) []string {
	indent := ""
	measured := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		leading := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if !measured {
			indent, measured = leading, true
			continue
		}
		indent = commonPrefix(indent, leading)
	}
	if indent == "" {
		return lines
	}
	shifted := make([]string, len(lines))
	for index, line := range lines {
		shifted[index] = strings.TrimPrefix(line, indent)
	}
	return shifted
}

// commonPrefix returns the leading run of characters left and right share.
func commonPrefix(left, right string) string {
	limit := min(len(left), len(right))
	for index := range limit {
		if left[index] != right[index] {
			return left[:index]
		}
	}
	return left[:limit]
}

// execMethod matches the Exec entry point by name, in either the historical
// Client.Exec spelling or the rasql.Exec one. The word boundary keeps
// ExecRendered and a builder's own Exec out, since neither rejects RETURNING.
var execMethod = regexp.MustCompile(`\b(?:[Cc]lient|rasql)\.Exec\b`)

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
			"| Write with `RETURNING` | `client.QueryWrite(ctx, statement)` | `row.Dynamic` |",
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
			"| Write with `RETURNING` | `client.QueryWrite(ctx, statement)` | `row.Dynamic` |",
		reject: false,
	},
	{
		name:    "invented passage names ExecRendered only",
		passage: "`client.ExecRendered` runs a statement that is already rendered, which is how a compiled static template is executed.",
		reject:  false,
	},
	{
		name:    "invented prose states the rejection in the rasql.Exec spelling",
		passage: "`rasql.Exec` rejects a write carrying a `RETURNING` clause, so decode those rows with `rasql.QueryWriteOne[T]`.",
		reject:  false,
	},
	{
		name:    "invented prose names RETURNING without a QueryWrite route in the rasql.Exec spelling",
		passage: "`rasql.Exec` runs any `query.WriteStatement`, including one that carries a `RETURNING` clause.",
		reject:  true,
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

// renderInclude creates the body of an include block: the included source in a
// fence marked with its language, followed by a link to the file it came from.
// A region target links to its file, since a reader follows the link to run the
// example the region was taken from.
func renderInclude(target, contents string) string {
	file, _ := includeTarget(target)
	return fmt.Sprintf("```%s\n%s\n```\nsource: [%s](%s%s)\n", fenceLanguage(file), contents, file, sourceURLPrefix, file)
}

// fenceLanguage names the language a file's fence is marked with.
func fenceLanguage(file string) string {
	if strings.HasSuffix(file, ".sql") {
		return "sql"
	}
	return "go"
}
