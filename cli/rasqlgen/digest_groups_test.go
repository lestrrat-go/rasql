package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func TestOfflineDigestGroupsReportsAllFourClasses(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "schema.sql"), []byte("schema"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "query.sql"), []byte("SELECT 1"), 0o600))
	mapping := compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "custom", GoType: "string"}}}
	query := compilerlock.QueryDigestInput{ID: "query", SQL: compilerlock.SourceFile{Path: "query.sql", SHA256: digest([]byte("SELECT 1"))}, Operation: "select", Cardinality: "one"}
	source := compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "migrations", Identity: "fixture", Files: []compilerlock.SourceFile{{Path: "schema.sql", SHA256: digest([]byte("schema"))}}}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3.35"}}
	generation := compilerir.GoConfig{Package: "store", Output: "internal/store", Emitter: "legacy", Prune: true}
	digests, err := compilerlock.BuildDigests(compilerlock.DigestInputs{Source: source, Mappings: mapping, Queries: []compilerlock.QueryDigestInput{query}, Generation: generation})
	require.NoError(t, err)
	lock := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Source: source.Record, Engine: source.Engine, Queries: []compilerlock.QueryRecord{{ID: "query", SQL: query.SQL, Operation: query.Operation, Cardinality: query.Cardinality}}, Generation: compilerlock.GenerationRecord{Package: generation.Package, Output: generation.Output, Emitter: generation.Emitter, Prune: generation.Prune}, Digests: digests}
	settings := config{Package: "renamed", Output: "other", Prune: boolPtr(false), Mappings: []byte(`{"scalars":[{"name":"changed","match":{"logical_kind":"text"},"go_type":"int","codec":"changed"}]}`)}
	require.NoError(t, os.WriteFile(filepath.Join(root, "schema.sql"), []byte("changed schema"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "query.sql"), []byte("SELECT 2"), 0o600))
	require.Equal(t, []string{"generation", "mappings", "queries", "source"}, mustDigestGroups(t, root, settings, lock))
}

func mustDigestGroups(t *testing.T, root string, settings config, lock compilerlock.File) []string {
	t.Helper()
	groups, err := offlineDigestGroups(root, settings, lock)
	require.NoError(t, err)
	return groups
}

func boolPtr(v bool) *bool { return &v }
