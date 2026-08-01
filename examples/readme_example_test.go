package examples_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var readmeExample = regexp.MustCompile("(?s)<!-- INCLUDE\\((examples/[^)]+)\\) -->\\n```go\\n(.*?)\\n```\\nsource: .*?\\n<!-- END INCLUDE -->")

func TestReadmeExamplesMatchSource(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "README.md"))
	require.NoError(t, err)

	matches := readmeExample.FindAllStringSubmatch(string(contents), -1)
	require.NotEmpty(t, matches)
	for _, match := range matches {
		exampleContents, err := os.ReadFile(filepath.Join("..", match[1]))
		require.NoError(t, err)
		require.Equal(t, strings.TrimSuffix(string(exampleContents), "\n"), match[2], match[1])
	}
}
