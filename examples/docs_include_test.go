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

// fencedBlock matches a fenced code block, which holds example source rather
// than the prose that routes a reader to an API.
var fencedBlock = regexp.MustCompile("(?s)```.*?```")

// TestDocsQualifyExecWithReturningRule holds the documentation to the rule
// Client.Exec enforces: it rejects a write statement carrying a RETURNING
// clause, so prose that sends a caller to Exec must name that exception rather
// than route the caller into a failing call.
func TestDocsQualifyExecWithReturningRule(t *testing.T) {
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
			require.Contains(t, paragraph, "RETURNING",
				"%s names Client.Exec without stating that it rejects a RETURNING write; say which call reads those rows instead:\n%s",
				page, strings.TrimSpace(paragraph))
		}
	}
	require.NotZero(t, passages, "no Client.Exec passage found in the documentation")
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
