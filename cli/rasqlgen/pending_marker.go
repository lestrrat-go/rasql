package rasqlgen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const pendingMarkerName = ".rasql-update.pending.json"

type fileState struct {
	State  string `json:"state"`
	SHA256 string `json:"sha256,omitempty"`
}
type pendingEntry struct {
	Path    string    `json:"path"`
	Old     fileState `json:"old"`
	Desired fileState `json:"desired"`
}
type pendingMarker struct {
	Operation   string         `json:"operation"`
	OldLock     string         `json:"old_lock_sha256"`
	DesiredLock string         `json:"desired_lock_sha256"`
	Entries     []pendingEntry `json:"entries"`
}

func stateFor(path string) (fileState, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileState{State: "missing"}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	h := sha256.Sum256(b)
	return fileState{State: "present", SHA256: hex.EncodeToString(h[:])}, nil
}

func writePending(root, oldLock, desiredLock string, entries []pendingEntry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	m := pendingMarker{Operation: "schema-update", OldLock: oldLock, DesiredLock: desiredLock, Entries: entries}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(root, ".rasql-update.pending.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, filepath.Join(root, pendingMarkerName))
}

func removePending(root string) error {
	err := os.Remove(filepath.Join(root, pendingMarkerName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func validatePending(root string) error {
	b, err := os.ReadFile(filepath.Join(root, pendingMarkerName))
	if err != nil {
		return err
	}
	var marker pendingMarker
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return fmt.Errorf("rasql: invalid pending marker: %w", err)
	}
	if marker.Operation != "schema-update" || marker.DesiredLock == "" {
		return fmt.Errorf("rasql: invalid pending marker operation or lock")
	}
	for _, entry := range marker.Entries {
		if entry.Path == "" || (entry.Old.State != "missing" && entry.Old.State != "present") || (entry.Desired.State != "missing" && entry.Desired.State != "present") {
			return fmt.Errorf("rasql: invalid pending entry %q", entry.Path)
		}
		actual, err := stateFor(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil {
			return err
		}
		if actual != entry.Old && actual != entry.Desired {
			return fmt.Errorf("rasql: pending path %q has third state", entry.Path)
		}
	}
	return nil
}
