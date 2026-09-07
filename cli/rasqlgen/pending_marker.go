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
	"strings"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
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
	marker, err := readPending(root)
	if err != nil {
		return err
	}
	for _, entry := range marker.Entries {
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

func readPending(root string) (*pendingMarker, error) {
	b, err := os.ReadFile(filepath.Join(root, pendingMarkerName))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var marker pendingMarker
	if err := decoder.Decode(&marker); err != nil {
		return nil, fmt.Errorf("rasql: invalid pending marker: %w", err)
	}
	if marker.Operation != "schema-update" || marker.DesiredLock == "" || len(marker.Entries) == 0 {
		return nil, fmt.Errorf("rasql: invalid pending marker operation or lock")
	}
	seen := make(map[string]struct{}, len(marker.Entries))
	for _, entry := range marker.Entries {
		if entry.Path == "" || filepath.IsAbs(entry.Path) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path))) != entry.Path || strings.HasPrefix(entry.Path, "../") {
			return nil, fmt.Errorf("rasql: invalid pending entry %q", entry.Path)
		}
		if _, ok := seen[entry.Path]; ok {
			return nil, fmt.Errorf("rasql: duplicate pending entry %q", entry.Path)
		}
		seen[entry.Path] = struct{}{}
		if (entry.Old.State != "missing" && entry.Old.State != "present") || (entry.Desired.State != "missing" && entry.Desired.State != "present") {
			return nil, fmt.Errorf("rasql: invalid pending entry %q", entry.Path)
		}
		if entry.Old.State == "present" {
			if err := compilerlock.ValidateDigest(entry.Old.SHA256); err != nil {
				return nil, fmt.Errorf("rasql: invalid pending digest %q: %w", entry.Path, err)
			}
		}
		if entry.Desired.State == "present" {
			if err := compilerlock.ValidateDigest(entry.Desired.SHA256); err != nil {
				return nil, fmt.Errorf("rasql: invalid pending digest %q: %w", entry.Path, err)
			}
		}
	}
	return &marker, nil
}

func pendingEntries(entries []generate.PublicationEntry) []pendingEntry {
	out := make([]pendingEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, pendingEntry{Path: entry.Path, Old: markerState(entry.Old), Desired: markerState(entry.Desired)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func markerState(state generate.PublicationState) fileState {
	if !state.Present {
		return fileState{State: "missing"}
	}
	return fileState{State: "present", SHA256: state.SHA256}
}

func validatePendingAgainst(root string, marker pendingMarker, current []pendingEntry, desiredLock string) error {
	if marker.DesiredLock != desiredLock {
		return fmt.Errorf("rasql: pending marker desired lock differs")
	}
	want := append([]pendingEntry(nil), marker.Entries...)
	sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
	sort.Slice(current, func(i, j int) bool { return current[i].Path < current[j].Path })
	if len(want) != len(current) {
		return fmt.Errorf("rasql: pending marker publication set differs")
	}
	for i := range want {
		if want[i].Path != current[i].Path || want[i].Desired != current[i].Desired {
			return fmt.Errorf("rasql: pending marker publication set differs at %q", current[i].Path)
		}
		actual, err := stateFor(filepath.Join(root, filepath.FromSlash(current[i].Path)))
		if err != nil {
			return err
		}
		if actual != want[i].Old && actual != want[i].Desired {
			return fmt.Errorf("rasql: pending path %q has third state", current[i].Path)
		}
	}
	return nil
}
