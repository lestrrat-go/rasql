package rasqlgen_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoRunHelp(t *testing.T) {
	testCases := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "global",
			args:     []string{"-h"},
			expected: "Usage: rasqlgen <command> [flags]",
		},
		{
			name:     "bootstrap",
			args:     []string{"bootstrap", "-h"},
			expected: "Usage of bootstrap:",
		},
		{
			name:     "schema",
			args:     []string{"schema", "-h"},
			expected: "Usage of schema:",
		},
		{
			name:     "query",
			args:     []string{"query", "-h"},
			expected: "Usage of query:",
		},
		{
			name:     "init",
			args:     []string{"init", "-h"},
			expected: "Usage of init:",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			arguments := append([]string{"run", "./cmd/rasqlgen"}, testCase.args...)
			command := exec.CommandContext(t.Context(), "go", arguments...)
			command.Dir = filepath.Join("..", "..")
			output, err := command.CombinedOutput()
			require.NoError(t, err, string(output))
			require.Contains(t, string(output), testCase.expected)
		})
	}
}
