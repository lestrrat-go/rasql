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
