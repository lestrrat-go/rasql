package generate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPlanCommitPublicationPublishesLockAfterGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	store := Store{Package: "store", Root: root, Dir: dir, legacyDialect: dialect.SQLite(), legacyTables: []schema.TableDef{
		schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id")),
	}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)

	lockPath := filepath.Join(root, "rasql.lock.json")
	lockSource := []byte(`{"format":1}` + "\n")
	called := false
	publication := Publication{
		FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: lockSource, Mode: 0o600}},
		BeforeWrite: func(_ context.Context, got []PublicationEntry) error {
			called = true
			got[0].Path = "changed"
			got[0].Desired.SHA256 = strings.Repeat("0", sha256.Size*2)
			if _, err := os.Stat(filepath.Join(dir, "users_gen.go")); !os.IsNotExist(err) {
				return fmt.Errorf("generated output was written before callback")
			}
			return nil
		},
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	}
	require.NoError(t, plan.CommitPublication(t.Context(), publication))
	require.True(t, called)
	require.Equal(t, lockSource, mustReadPublicationFile(t, lockPath))
	require.FileExists(t, filepath.Join(dir, "users_gen.go"))
}

func TestPlanCommitPublicationRefusesWrongLockStateBeforeCallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	store := Store{Package: "store", Root: root, Dir: dir, legacyTables: []schema.TableDef{
		schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id")),
	}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	called := false
	err = plan.CommitPublication(t.Context(), Publication{
		FinalFiles:  []FinalFile{{Path: "../outside.json", Source: []byte("new\n"), Mode: 0o600}},
		BeforeWrite: func(context.Context, []PublicationEntry) error { called = true; return nil },
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	})
	require.Error(t, err)
	require.False(t, called)
	require.NoFileExists(t, filepath.Join(dir, "users_gen.go"))
}

func TestPlanCommitPublicationReportsSortedClonedStates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	store := Store{Package: "store", Root: root, Dir: dir, legacyDialect: dialect.SQLite(), legacyTables: []schema.TableDef{
		schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id")),
	}}
	require.NoError(t, store.Write())
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	var got []PublicationEntry
	publication := Publication{
		FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: []byte("lock\n"), Mode: 0o600}},
		BeforeWrite: func(_ context.Context, entries []PublicationEntry) error {
			got = append([]PublicationEntry(nil), entries...)
			entries[0].Path = "mutated"
			return nil
		},
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	}
	require.NoError(t, plan.CommitPublication(t.Context(), publication))
	files := plan.Files()
	expected := make(map[string]PublicationEntry, len(files)+1)
	for _, file := range files {
		digest := sha256.Sum256(file.Source)
		path, err := filepath.Rel(root, file.Path)
		require.NoError(t, err)
		expected[filepath.ToSlash(path)] = PublicationEntry{Path: filepath.ToSlash(path), Old: PublicationState{Present: true, SHA256: mustSHA256File(t, file.Path)}, Desired: PublicationState{Present: true, SHA256: hex.EncodeToString(digest[:])}}
	}
	digest := sha256.Sum256([]byte("lock\n"))
	expected["rasql.lock.json"] = PublicationEntry{Path: "rasql.lock.json", Desired: PublicationState{Present: true, SHA256: hex.EncodeToString(digest[:])}}
	require.Len(t, got, len(expected))
	for i := 1; i < len(got); i++ {
		require.LessOrEqual(t, got[i-1].Path, got[i].Path)
	}
	for _, entry := range got {
		require.Equal(t, expected[entry.Path], entry)
	}
	require.NotEqual(t, "mutated", got[0].Path)
}

func TestPlanCommitPublicationRefusesChangedOrphanBytes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	users := commitTestUsersDef()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, WritePackage("store", dir, users))
	orphan := filepath.Join(dir, "old_gen.go")
	old := []byte(genfile.Marker + "\n\npackage store\n")
	require.NoError(t, os.WriteFile(orphan, old, 0o600))
	store := Store{Package: "store", Root: root, Dir: dir, Prune: true, legacyTables: []schema.TableDef{users}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	changed := []byte(genfile.Marker + "\n\npackage store\n// changed\n")
	err = plan.CommitPublication(t.Context(), Publication{
		FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: []byte("lock\n")}},
		BeforeWrite: func(context.Context, []PublicationEntry) error {
			return os.WriteFile(orphan, changed, 0o600)
		},
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	})
	require.Error(t, err)
	require.Equal(t, changed, mustReadPublicationFile(t, orphan))
}

func TestPlanCommitPublicationRecoversMarkerOwnedMissingAndPresent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	old := []byte(genfile.Marker + "\n\npackage store\n")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old_gen.go"), old, 0o600))
	store := Store{Package: "store", Root: root, Dir: dir, Prune: true, legacyDialect: dialect.SQLite(), legacyTables: []schema.TableDef{
		schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id")),
	}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	hash := sha256.Sum256(old)
	missingHash := sha256.Sum256([]byte("already gone"))
	publication := Publication{
		FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: []byte("lock\n"), Mode: 0o600}},
		RecoveryDeletions: []RecoveryDeletion{
			{Path: "store/old_gen.go", OldSHA256: hex.EncodeToString(hash[:])},
			{Path: "store/missing_gen.go", OldSHA256: hex.EncodeToString(missingHash[:])},
		},
		BeforeWrite: func(context.Context, []PublicationEntry) error { return nil },
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	}
	require.NoError(t, plan.CommitPublication(t.Context(), publication))
	require.NoFileExists(t, filepath.Join(dir, "old_gen.go"))
}

func TestPlanCommitPublicationCancellationDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	store := Store{Package: "store", Root: root, Dir: filepath.Join(root, "store"), legacyDialect: dialect.SQLite(), legacyTables: []schema.TableDef{
		schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id")),
	}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err = plan.CommitPublication(ctx, Publication{
		FinalFiles:  []FinalFile{{Path: "rasql.lock.json", Source: []byte("lock\n"), Mode: 0o600}},
		BeforeWrite: func(context.Context, []PublicationEntry) error { called = true; return nil },
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
	require.NoDirExists(t, filepath.Join(root, "store"))
}

func TestPlanCommitPublicationOrderIncludesRecoveryBeforeAggregators(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	users := commitTestUsersDef()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, WritePackage("store", dir, users))
	orphan := filepath.Join(dir, "old_gen.go")
	require.NoError(t, os.WriteFile(orphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
	queryPath := filepath.Join(root, "q.sql")
	require.NoError(t, os.WriteFile(queryPath, []byte("SELECT 1"), 0o600))
	store := Store{Package: "store", Root: root, Dir: dir, Prune: true, legacyDialect: dialect.PostgreSQL(), legacyTables: []schema.TableDef{users}, legacyQueries: []legacyQuery{{Input: queryPath, Function: "Q", Output: "q_gen.go"}}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	var sequence []string
	oldWrite, oldRemove, oldFinal := writeGeneratedFile, removeGeneratedFile, writeFinalFile
	t.Cleanup(func() { writeGeneratedFile, removeGeneratedFile, writeFinalFile = oldWrite, oldRemove, oldFinal })
	writeGeneratedFile = func(root *os.Root, name string, source []byte) error {
		sequence = append(sequence, "write:"+name)
		return oldWrite(root, name, source)
	}
	removeGeneratedFile = func(root *os.Root, name string) error {
		sequence = append(sequence, "delete:"+name)
		return oldRemove(root, name)
	}
	writeFinalFile = func(root *os.Root, name string, source []byte, mode fs.FileMode) error {
		sequence = append(sequence, "final:"+name)
		return oldFinal(root, name, source, mode)
	}
	publication := Publication{FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: []byte("lock\n"), Mode: 0o600}}, BeforeWrite: func(context.Context, []PublicationEntry) error { sequence = append(sequence, "before"); return nil }, AfterVerify: func(context.Context, []PublicationEntry) error { sequence = append(sequence, "after"); return nil }}
	require.NoError(t, plan.CommitPublication(t.Context(), publication))
	require.Equal(t, []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go", "write:schema_gen.go", "write:schema_gen_test.go", "final:rasql.lock.json", "after"}, sequence)
}

func TestPlanCommitPublicationFaultsStopAtTheInjectedPhase(t *testing.T) {
	fault := fmt.Errorf("injected publication fault")
	tests := []struct {
		name       string
		phase      string
		failName   string
		wantPrefix []string
	}{
		{name: "before", phase: "before", wantPrefix: []string{"before"}},
		{name: "ordinary write", phase: "write", failName: "q_gen.go", wantPrefix: []string{"before", "write:q_gen.go"}},
		{name: "orphan delete", phase: "delete", failName: "old_gen.go", wantPrefix: []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go"}},
		{name: "first aggregator", phase: "write", failName: "schema_gen.go", wantPrefix: []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go", "write:schema_gen.go"}},
		{name: "second aggregator", phase: "write", failName: "schema_gen_test.go", wantPrefix: []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go", "write:schema_gen.go", "write:schema_gen_test.go"}},
		{name: "final write", phase: "final", failName: "rasql.lock.json", wantPrefix: []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go", "write:schema_gen.go", "write:schema_gen_test.go", "final:rasql.lock.json"}},
		{name: "verify", phase: "verify", wantPrefix: []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go", "write:schema_gen.go", "write:schema_gen_test.go", "final:rasql.lock.json", "verify"}},
		{name: "after verify", phase: "after", wantPrefix: []string{"before", "write:q_gen.go", "write:users_gen.go", "delete:old_gen.go", "write:schema_gen.go", "write:schema_gen_test.go", "final:rasql.lock.json", "verify", "after"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "store")
			users := commitTestUsersDef()
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, WritePackage("store", dir, users))
			orphan := filepath.Join(dir, "old_gen.go")
			oldOrphan := []byte(genfile.Marker + "\n\npackage store\n")
			require.NoError(t, os.WriteFile(orphan, oldOrphan, 0o600))
			queryPath := filepath.Join(root, "q.sql")
			require.NoError(t, os.WriteFile(queryPath, []byte("SELECT 1"), 0o600))
			oldLock := []byte("old lock\n")
			lockPath := filepath.Join(root, "rasql.lock.json")
			require.NoError(t, os.WriteFile(lockPath, oldLock, 0o600))
			store := Store{Package: "store", Root: root, Dir: dir, Prune: true, legacyDialect: dialect.PostgreSQL(), legacyTables: []schema.TableDef{users}, legacyQueries: []legacyQuery{{Input: queryPath, Function: "Q", Output: "q_gen.go"}}}
			plan, err := store.PlanContext(t.Context())
			require.NoError(t, err)
			oldFiles := make(map[string][]byte)
			desiredFiles := make(map[string][]byte)
			for _, file := range plan.Files() {
				if data, readErr := os.ReadFile(file.Path); readErr == nil {
					oldFiles[filepath.Base(file.Path)] = data
				} else {
					require.ErrorIs(t, readErr, os.ErrNotExist)
				}
				desiredFiles[filepath.Base(file.Path)] = file.Source
			}
			oldFiles["old_gen.go"] = oldOrphan
			oldFiles["rasql.lock.json"] = oldLock
			desiredFiles["rasql.lock.json"] = []byte("new lock\n")
			var sequence []string
			beforeState, afterState := false, false
			verifyRecorded := false
			oldWrite, oldRemove, oldFinal, oldVerify := writeGeneratedFile, removeGeneratedFile, writeFinalFile, verifyFinalState
			t.Cleanup(func() {
				writeGeneratedFile, removeGeneratedFile, writeFinalFile, verifyFinalState = oldWrite, oldRemove, oldFinal, oldVerify
			})
			writeGeneratedFile = func(root *os.Root, name string, source []byte) error {
				sequence = append(sequence, "write:"+name)
				if test.phase == "write" && test.failName == name {
					return fault
				}
				return oldWrite(root, name, source)
			}
			removeGeneratedFile = func(root *os.Root, name string) error {
				sequence = append(sequence, "delete:"+name)
				if test.phase == "delete" && test.failName == name {
					return fault
				}
				return oldRemove(root, name)
			}
			writeFinalFile = func(root *os.Root, name string, source []byte, mode fs.FileMode) error {
				sequence = append(sequence, "final:"+name)
				if test.phase == "final" && test.failName == name {
					return fault
				}
				return oldFinal(root, name, source, mode)
			}
			verifyFinalState = func(root *os.Root, name string, desired PublicationState) error {
				if !verifyRecorded {
					sequence = append(sequence, "verify")
					verifyRecorded = true
				}
				if test.phase == "verify" {
					return fault
				}
				return oldVerify(root, name, desired)
			}
			publication := Publication{
				FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: []byte("new lock\n"), Mode: 0o600}},
				BeforeWrite: func(context.Context, []PublicationEntry) error {
					sequence = append(sequence, "before")
					beforeState = true
					if test.phase == "before" {
						return fault
					}
					return nil
				},
				AfterVerify: func(context.Context, []PublicationEntry) error {
					sequence = append(sequence, "after")
					afterState = true
					if test.phase == "after" {
						return fault
					}
					return nil
				},
			}
			err = plan.CommitPublication(t.Context(), publication)
			require.ErrorIs(t, err, fault)
			require.Equal(t, test.wantPrefix, sequence)
			require.True(t, beforeState)
			require.Equal(t, test.phase == "after", afterState)
			for name, old := range oldFiles {
				path := filepath.Join(dir, name)
				if name == "rasql.lock.json" {
					path = lockPath
				}
				if _, statErr := os.Stat(path); statErr == nil {
					requireWholeFileOneOf(t, path, old, desiredFiles[name])
				} else {
					require.ErrorIs(t, statErr, os.ErrNotExist)
				}
			}
			if test.phase == "final" {
				require.Equal(t, oldLock, mustReadPublicationFile(t, lockPath))
			}
			if test.phase == "verify" || test.phase == "after" {
				require.Equal(t, []byte("new lock\n"), mustReadPublicationFile(t, lockPath))
			}
		})
	}
}

func TestPlanCommitPublicationRejectsUnsafeFinalTargetsBeforeCallback(t *testing.T) {
	tests := []struct {
		name  string
		path  func(root, dir string) string
		setup func(t *testing.T, root, dir, target string)
	}{
		{name: "existing symlink", path: func(root, dir string) string { return "rasql.lock.json" }, setup: func(t *testing.T, root, dir, target string) {
			outside := filepath.Join(root, "outside.json")
			require.NoError(t, os.WriteFile(outside, []byte("outside\n"), 0o600))
			require.NoError(t, os.Symlink(outside, target))
		}},
		{name: "existing directory", path: func(root, dir string) string { return "rasql.lock.json" }, setup: func(t *testing.T, root, dir, target string) {
			require.NoError(t, os.Mkdir(target, 0o700))
		}},
		{name: "generated collision", path: func(root, dir string) string { return "store/users_gen.go" }, setup: func(t *testing.T, root, dir, target string) {}},
		{name: "parent escapes root", path: func(root, dir string) string { return "link/rasql.lock.json" }, setup: func(t *testing.T, root, dir, target string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "store")
			store := Store{Package: "store", Root: root, Dir: dir, legacyTables: []schema.TableDef{commitTestUsersDef()}}
			plan, err := store.PlanContext(t.Context())
			require.NoError(t, err)
			target := filepath.Join(root, filepath.FromSlash(test.path(root, dir)))
			if test.name != "generated collision" {
				test.setup(t, root, dir, target)
			}
			called := false
			err = plan.CommitPublication(t.Context(), Publication{
				FinalFiles:  []FinalFile{{Path: test.path(root, dir), Source: []byte("lock\n")}},
				BeforeWrite: func(context.Context, []PublicationEntry) error { called = true; return nil },
				AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
			})
			require.Error(t, err)
			require.False(t, called)
			require.NoFileExists(t, filepath.Join(dir, "users_gen.go"))
		})
	}
}

func TestPlanCommitPublicationRefusesFinalParentRetarget(t *testing.T) {
	root := t.TempDir()
	finalDir := filepath.Join(root, "final")
	require.NoError(t, os.Mkdir(finalDir, 0o700))
	store := Store{Package: "store", Root: root, Dir: filepath.Join(root, "store"), legacyTables: []schema.TableDef{commitTestUsersDef()}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	oldResolve := resolvePublication
	t.Cleanup(func() { resolvePublication = oldResolve })
	resolvePublication = func(path string) (string, fs.FileInfo, error) {
		resolved, info, err := oldResolve(path)
		if err != nil || filepath.Dir(path) != finalDir {
			return resolved, info, err
		}
		oldDir := finalDir + ".old"
		if err := os.Rename(finalDir, oldDir); err != nil {
			return resolved, info, err
		}
		if err := os.Mkdir(finalDir, 0o700); err != nil {
			return resolved, info, err
		}
		return resolved, info, nil
	}
	called := false
	err = plan.CommitPublication(t.Context(), Publication{
		FinalFiles:  []FinalFile{{Path: "final/rasql.lock.json", Source: []byte("lock\n")}},
		BeforeWrite: func(context.Context, []PublicationEntry) error { called = true; return nil },
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	})
	require.Error(t, err)
	require.False(t, called)
	require.NoFileExists(t, filepath.Join(finalDir, "rasql.lock.json"))
	require.NoFileExists(t, filepath.Join(finalDir+".old", "rasql.lock.json"))
}

func mustReadPublicationFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func mustSHA256File(t *testing.T, path string) string {
	t.Helper()
	digest := sha256.Sum256(mustReadPublicationFile(t, path))
	return hex.EncodeToString(digest[:])
}

func requireWholeFileOneOf(t *testing.T, path string, choices ...[]byte) {
	t.Helper()
	got := mustReadPublicationFile(t, path)
	for _, choice := range choices {
		if bytes.Equal(got, choice) {
			return
		}
	}
	t.Fatalf("%s contains unexpected bytes %q", path, got)
}
