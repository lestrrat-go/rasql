package compilerlock_test

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func TestAcceptanceFixturesAreCanonicalAndPhysicallyComplete(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			path := filepath.Join("testdata", "v1", dialect+".json")
			encoded, err := os.ReadFile(path)
			require.NoError(t, err)
			lock, err := compilerlock.Decode(encoded)
			require.NoError(t, err)
			canonical, err := compilerlock.Encode(lock)
			require.NoError(t, err)
			require.Equal(t, canonical, encoded, "fixture must be the exact Encode output")

			catalogBytes, err := os.ReadFile(filepath.Join("..", "compilerir", "testdata", dialect, "catalog.json"))
			require.NoError(t, err)
			var expected compilerir.PhysicalCatalog
			require.NoError(t, json.Unmarshal(catalogBytes, &expected))
			expected = canonicalPhysical(expected)
			require.Equal(t, expected, compilerlock.ToPhysical(lock), "lock conversion must preserve every physical fact")
			require.NotEmpty(t, expected.Objects)
			require.NotEmpty(t, lock.Queries)
			require.NotEmpty(t, lock.Generation.Objects)
			require.NotEmpty(t, lock.Generation.Queries)
		})
	}
}

func canonicalPhysical(c compilerir.PhysicalCatalog) compilerir.PhysicalCatalog {
	c = c.Clone()
	for i := range c.Objects {
		sort.Slice(c.Objects[i].Constraints, func(a, b int) bool { return c.Objects[i].Constraints[a].Name < c.Objects[i].Constraints[b].Name })
		if len(c.Objects[i].Indexes) == 0 {
			c.Objects[i].Indexes = nil
		}
	}
	return c
}

func TestAcceptanceFixturesCoverQueryCertaintyAndEngineEvidence(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		b, err := os.ReadFile(filepath.Join("testdata", "v1", dialect+".json"))
		require.NoError(t, err)
		f, err := compilerlock.Decode(b)
		require.NoError(t, err)
		certainties := map[compilerir.Certainty]bool{}
		for _, q := range f.Queries {
			require.Equal(t, f.Engine.Dialect, q.Evidence.Dialect)
			require.Equal(t, f.Engine.Profile, q.Evidence.Profile)
			for _, v := range append(append([]compilerlock.ValueRecord{}, q.Parameters...), q.Results...) {
				certainties[v.TypeCertainty] = true
				certainties[v.NullabilityCertainty] = true
			}
		}
		require.True(t, certainties[compilerir.CertaintyKnown])
		require.True(t, certainties[compilerir.CertaintyDeclared])
		require.True(t, certainties[compilerir.CertaintyUnknown])
	}
}

func TestAcceptanceFixtureCleanCopiesHaveIdenticalBytes(t *testing.T) {
	src := filepath.Join("testdata", "v1", "postgresql.json")
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	left, right := t.TempDir(), t.TempDir()
	for _, dir := range []string{left, right} {
		f, err := compilerlock.Decode(b)
		require.NoError(t, err)
		encoded, err := compilerlock.Encode(f)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.lock.json"), encoded, 0o600))
	}
	a, err := os.ReadFile(filepath.Join(left, "schema.lock.json"))
	require.NoError(t, err)
	c, err := os.ReadFile(filepath.Join(right, "schema.lock.json"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(a, c))
}

func TestAcceptanceNormalizationPreservesPositionalArrays(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "v1", "postgresql.json"))
	require.NoError(t, err)
	base, err := compilerlock.Decode(b)
	require.NoError(t, err)
	want, err := compilerlock.Encode(base)
	require.NoError(t, err)
	for seed := int64(1); seed <= 100; seed++ {
		f := base
		r := rand.New(rand.NewSource(seed))
		shuffle := func(n int, swap func(int, int)) {
			for i := n - 1; i > 0; i-- {
				swap(i, r.Intn(i+1))
			}
		}
		shuffle(len(f.Source.Files), func(i, j int) { f.Source.Files[i], f.Source.Files[j] = f.Source.Files[j], f.Source.Files[i] })
		shuffle(len(f.Catalog.Objects), func(i, j int) {
			f.Catalog.Objects[i], f.Catalog.Objects[j] = f.Catalog.Objects[j], f.Catalog.Objects[i]
		})
		shuffle(len(f.Queries), func(i, j int) { f.Queries[i], f.Queries[j] = f.Queries[j], f.Queries[i] })
		shuffle(len(f.Generation.Objects), func(i, j int) {
			f.Generation.Objects[i], f.Generation.Objects[j] = f.Generation.Objects[j], f.Generation.Objects[i]
		})
		shuffle(len(f.Generation.Queries), func(i, j int) {
			f.Generation.Queries[i], f.Generation.Queries[j] = f.Generation.Queries[j], f.Generation.Queries[i]
		})
		before := f.Catalog.Objects[0].Columns
		got, err := compilerlock.Encode(f)
		require.NoError(t, err)
		require.Equal(t, want, got, "seed %d", seed)
		require.True(t, reflect.DeepEqual(before, f.Catalog.Objects[0].Columns), "Encode must not mutate positional column arrays")
	}
}
