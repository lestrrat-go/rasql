package compilerlock_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func TestGoldenV1FixturesRefuseMissingMappingsAndV2RoundTrips(t *testing.T) {
	for _, name := range []string{"postgresql", "mysql", "sqlite"} {
		b, err := os.ReadFile(filepath.Join("testdata", "v1", name+".json"))
		require.NoError(t, err)
		_, err = compilerlock.Decode(b)
		require.ErrorContains(t, err, "lacks mapping records")
	}
	for _, name := range []string{"postgresql", "mysql", "sqlite"} {
		b, err := os.ReadFile(filepath.Join("testdata", "v2", name+".json"))
		require.NoError(t, err)
		f, err := compilerlock.Decode(b)
		require.NoError(t, err)
		encoded, err := compilerlock.Encode(f)
		require.NoError(t, err)
		upgraded, err := compilerlock.Upgrade(encoded)
		require.NoError(t, err)
		require.Equal(t, encoded, upgraded)
	}
	future, err := os.ReadFile(filepath.Join("testdata", "v1", "future.json"))
	require.NoError(t, err)
	_, err = compilerlock.Upgrade(future)
	require.Error(t, err)
}

func TestEncodeDoesNotMutateInputAndPreservesNativeEmpty(t *testing.T) {
	empty := []string{}
	c := compilerir.PhysicalCatalog{
		Engine: compilerir.EngineIdentity{Dialect: "sqlite", Profile: "sqlite-3"},
		Objects: []compilerir.PhysicalObject{{
			ID: "t", Kind: "table", Name: "t",
			Columns: []compilerir.PhysicalColumn{{Name: "x", Ordinal: 0, LogicalKind: "native", Native: &compilerir.NativeType{Dialect: "sqlite", Name: "x", Kind: "other", Arguments: empty}}},
			Indexes: []compilerir.PhysicalIndex{{Name: "idx", KeyForm: "keys", Parts: []compilerir.IndexPart{{ExpressionSQL: "x"}}}},
		}},
	}
	f := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "x", Source: compilerlock.SourceRecord{Kind: "external", Identity: "x"}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}, Catalog: compilerlock.FromPhysical(c), Generation: compilerlock.GenerationRecord{Package: "p", Output: "o", Emitter: "compact"}, Digests: compilerlock.Digests{Source: strings.Repeat("a", 64), Mappings: strings.Repeat("b", 64), Queries: strings.Repeat("c", 64), Generation: strings.Repeat("d", 64)}}
	before := f
	encoded, err := compilerlock.Encode(f)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(before, f))
	decoded, err := compilerlock.Decode(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.Catalog.Objects[0].Columns[0].Native.Arguments)
	require.Empty(t, *decoded.Catalog.Objects[0].Columns[0].Native.Arguments)
	require.Equal(t, "keys", decoded.Catalog.Objects[0].Indexes[0].KeyForm)
}

func TestSourceSnapshotRevalidation(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("create table t (id int);"), 0600))
	s, err := compilerlock.SnapshotSourceFile(dir, "schema.sql")
	require.NoError(t, err)
	require.Equal(t, "schema.sql", s.Record().Path)
	require.NoError(t, s.Revalidate())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("changed"), 0600))
	require.ErrorIs(t, s.Revalidate(), compilerlock.ErrSourceChanged)
	_, err = compilerlock.SourceBytes(filepath.Join(dir, "missing"))
	require.Error(t, err)
}
