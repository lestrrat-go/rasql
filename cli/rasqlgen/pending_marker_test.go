package rasqlgen

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/stretchr/testify/require"
)

func TestPendingMarkerAcceptsAllReplayStates(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "internal", "store", "users_gen.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	old := []byte("old")
	desired := []byte("desired")
	require.NoError(t, os.WriteFile(path, old, 0o600))
	oldHash := digest(old)
	desiredHash := digest(desired)
	marker := pendingMarker{Operation: "schema-update", OldLock: digest([]byte("old-lock")), DesiredLock: digest([]byte("new-lock")), Entries: []pendingEntry{{Path: "internal/store/users_gen.go", Old: fileState{State: "present", SHA256: oldHash}, Desired: fileState{State: "present", SHA256: desiredHash}}}}

	for _, state := range []struct {
		name string
		data []byte
	}{
		{name: "all old", data: old},
		{name: "mixed desired", data: desired},
		{name: "all desired", data: desired},
	} {
		t.Run(state.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, state.data, 0o600))
			entries := []pendingEntry{{Path: marker.Entries[0].Path, Old: marker.Entries[0].Old, Desired: marker.Entries[0].Desired}}
			require.NoError(t, validatePendingAgainst(root, marker, entries, marker.DesiredLock))
		})
	}
}

func TestPendingMarkerAcceptsAlreadyMissingDeletion(t *testing.T) {
	root := t.TempDir()
	marker := pendingMarker{Operation: "schema-update", DesiredLock: digest([]byte("new-lock")), Entries: []pendingEntry{{Path: "internal/store/old_gen.go", Old: fileState{State: "present", SHA256: digest([]byte("old"))}, Desired: fileState{State: "missing"}}}}
	entries := append([]pendingEntry(nil), marker.Entries...)
	require.NoError(t, validatePendingAgainst(root, marker, entries, marker.DesiredLock))
}

func TestPendingMarkerRejectsThirdStateWithoutMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "internal", "store", "users_gen.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("third"), 0o600))
	marker := pendingMarker{Operation: "schema-update", DesiredLock: digest([]byte("new-lock")), Entries: []pendingEntry{{Path: "internal/store/users_gen.go", Old: fileState{State: "missing"}, Desired: fileState{State: "present", SHA256: digest([]byte("desired"))}}}}
	entries := pendingEntries([]generate.PublicationEntry{{Path: marker.Entries[0].Path, Old: generate.PublicationState{}, Desired: generate.PublicationState{Present: true, SHA256: marker.Entries[0].Desired.SHA256}}})
	require.Error(t, validatePendingAgainst(root, marker, entries, marker.DesiredLock))
	require.Equal(t, []byte("third"), mustRead(path))
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func mustRead(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return b
}
