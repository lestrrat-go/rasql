package generate

import (
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
	store := Store{Package: "store", Root: root, Dir: dir, Dialect: dialect.SQLite(), Tables: []schema.TableDef{
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
	store := Store{Package: "store", Root: root, Dir: dir, Tables: []schema.TableDef{
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
	store := Store{Package: "store", Root: root, Dir: dir, Dialect: dialect.SQLite(), Tables: []schema.TableDef{
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
	require.GreaterOrEqual(t, len(got), 4)
	for i := 1; i < len(got); i++ {
		require.LessOrEqual(t, got[i-1].Path, got[i].Path)
	}
	require.NotEqual(t, "mutated", got[0].Path)
}

func TestPlanCommitPublicationRecoversMarkerOwnedMissingAndPresent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	old := []byte(genfile.Marker + "\n\npackage store\n")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old_gen.go"), old, 0o600))
	store := Store{Package: "store", Root: root, Dir: dir, Prune: true, Dialect: dialect.SQLite(), Tables: []schema.TableDef{
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
	store := Store{Package: "store", Root: root, Dir: filepath.Join(root, "store"), Dialect: dialect.SQLite(), Tables: []schema.TableDef{
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
	store := Store{Package: "store", Root: root, Dir: dir, Prune: true, Dialect: dialect.PostgreSQL(), Tables: []schema.TableDef{users}, Queries: []Query{{Input: queryPath, Function: "Q", Output: "q_gen.go"}}}
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

func mustReadPublicationFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
