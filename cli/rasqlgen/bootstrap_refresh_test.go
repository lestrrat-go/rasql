package rasqlgen

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsDirectoryPattern(t *testing.T) {
	tests := map[string]string{
		"internal/tables":      "./internal/tables",
		"./internal/tables":    "./internal/tables",
		"../internal/tables":   "../internal/tables",
		".":                    ".",
		"..":                   "..",
		"/abs/internal/tables": "/abs/internal/tables",
	}
	for input, want := range tests {
		require.Equal(t, want, asDirectoryPattern(input), "input %q", input)
	}
}
