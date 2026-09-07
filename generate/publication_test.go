package generate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
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
	hash := sha256.Sum256(lockSource)
	entries := []PublicationEntry{{
		Path:    "rasql.lock.json",
		Old:     PublicationState{Present: false},
		Desired: PublicationState{Present: true, SHA256: fmt.Sprintf("%x", hash[:])},
	}}
	called := false
	publication := Publication{
		FinalFiles: []FinalFile{{Path: "rasql.lock.json", Source: lockSource, Mode: 0o600}},
		BeforeWrite: func(_ context.Context, got []PublicationEntry) error {
			called = true
			got[0].Path = "changed"
			if _, err := os.Stat(filepath.Join(dir, "users_gen.go")); !os.IsNotExist(err) {
				return fmt.Errorf("generated output was written before callback")
			}
			return nil
		},
		AfterVerify: func(context.Context, []PublicationEntry) error { return nil },
	}
	require.NoError(t, plan.CommitPublication(t.Context(), publication))
	require.True(t, called)
	require.Equal(t, "rasql.lock.json", entries[0].Path)
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

func mustReadPublicationFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
