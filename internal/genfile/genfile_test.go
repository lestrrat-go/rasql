package genfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// handWrittenSource is a file somebody wrote by hand that happens to sit
// where generated output would land. Nothing about it says rasqlgen, which
// is the whole point: the destination must survive.
const handWrittenSource = "package store\n\n// Written by hand. Losing this file loses the only copy.\nfunc Keep() bool { return true }\n"

// writtenSource is a minimal file with the shape rasqlgen writes: the
// generated marker on its first line, then the package clause.
const writtenSource = Marker + "\n\npackage generated\n"

// TestWriteRefusesHandWrittenDestination covers the guard at the single
// write door every generated file goes through. A destination that does not
// exist, or that carries the generated marker as its whole first line, is
// written; anything else is refused with its own contents left exactly as
// they were.
func TestWriteRefusesHandWrittenDestination(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		existing string
		absent   bool
		refused  bool
	}{
		{name: "destination does not exist", absent: true},
		{name: "destination carries the marker", existing: Marker + "\n\npackage stale\n"},
		{name: "destination is only the marker", existing: Marker},
		{name: "destination is hand written", existing: handWrittenSource, refused: true},
		{name: "destination is empty", existing: "", refused: true},
		{name: "destination has the marker with trailing text", existing: Marker + " and more\n\npackage store\n", refused: true},
		{name: "destination has the marker on a later line", existing: "package store\n\n" + Marker + "\n", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			output := filepath.Join(directory, "output_gen.go")
			if !testCase.absent {
				require.NoError(t, os.WriteFile(output, []byte(testCase.existing), 0o600))
			}

			err := Write(output, []byte(writtenSource))
			if !testCase.refused {
				require.NoError(t, err)
				source, err := os.ReadFile(output)
				require.NoError(t, err)
				require.Equal(t, writtenSource, string(source))
				return
			}
			require.ErrorContains(t, err, "refusing to overwrite")
			require.ErrorContains(t, err, output)
			source, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, testCase.existing, string(source))
		})
	}
}
