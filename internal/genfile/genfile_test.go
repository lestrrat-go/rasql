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
var writtenSource = Schema.Marker() + "\n\npackage generated\n"

// TestWriteRefusesHandWrittenDestination covers the guard at the single
// write door every generated file goes through. A destination that does not
// exist, or that carries the writing family's own marker as its whole first
// line, is written; anything else is refused with its own contents left
// exactly as they were.
func TestWriteRefusesHandWrittenDestination(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		existing string
		absent   bool
		refused  bool
	}{
		{name: "destination does not exist", absent: true},
		{name: "destination carries the marker", existing: Schema.Marker() + "\n\npackage stale\n"},
		{name: "destination is only the marker", existing: Schema.Marker()},
		{name: "destination is hand written", existing: handWrittenSource, refused: true},
		{name: "destination is empty", existing: "", refused: true},
		{name: "destination has the marker with trailing text", existing: Schema.Marker() + " and more\n\npackage store\n", refused: true},
		{name: "destination has the marker on a later line", existing: "package store\n\n" + Schema.Marker() + "\n", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			output := filepath.Join(directory, "output_gen.go")
			if !testCase.absent {
				require.NoError(t, os.WriteFile(output, []byte(testCase.existing), 0o600))
			}

			err := Write(Schema, output, []byte(writtenSource))
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

// TestResolveDestinationAcceptsItsOwnFamilysMarker confirms every artifact
// family may replace a destination that already carries its own marker.
func TestResolveDestinationAcceptsItsOwnFamilysMarker(t *testing.T) {
	for _, artifact := range allArtifacts {
		t.Run(artifact.String(), func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "output_gen.go")
			require.NoError(t, os.WriteFile(path, []byte(artifact.Marker()+"\n\npackage stale\n"), 0o600))

			destination, err := ResolveDestination(artifact, path)
			require.NoError(t, err)
			require.Equal(t, path, destination)
		})
	}
}

// TestResolveDestinationRejectsAnotherFamilysMarker confirms one family's
// writer refuses a destination that carries a different family's marker,
// naming both families in the error. This is what stops
// `rasqlgen query -output` from destroying a bootstrap descriptor, or a
// mistyped `schema -output` from doing the same.
func TestResolveDestinationRejectsAnotherFamilysMarker(t *testing.T) {
	for _, owner := range allArtifacts {
		for _, writer := range allArtifacts {
			if owner == writer {
				continue
			}
			t.Run(owner.String()+"_vs_"+writer.String(), func(t *testing.T) {
				directory := t.TempDir()
				path := filepath.Join(directory, "output_gen.go")
				original := owner.Marker() + "\n\npackage stale\n"
				require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

				_, err := ResolveDestination(writer, path)
				require.Error(t, err)
				require.ErrorContains(t, err, owner.String())
				require.ErrorContains(t, err, writer.String())

				source, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, original, string(source))
			})
		}
	}
}

// TestResolveDestinationRejectsThePreSplitMarker confirms every family
// refuses a destination still carrying the single marker every rasqlgen
// command wrote before each family gained its own, and tells the reader to
// delete and regenerate rather than guessing which command wrote it.
func TestResolveDestinationRejectsThePreSplitMarker(t *testing.T) {
	for _, artifact := range allArtifacts {
		t.Run(artifact.String(), func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "output_gen.go")
			original := preSplitMarker + "\n\npackage stale\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

			_, err := ResolveDestination(artifact, path)
			require.Error(t, err)
			require.ErrorContains(t, err, "delete it and regenerate")

			source, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, original, string(source))
		})
	}
}
