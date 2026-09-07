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
	// The checked-in PostgreSQL catalog has one index and no exclusion
	// constraints. Add distinguishable entries so every unordered nested
	// collection exercised below has a meaningful shuffle.
	base.Catalog.Objects[0].Indexes = append(base.Catalog.Objects[0].Indexes, compilerlock.IndexRecord{
		Name: "users_note", KeyForm: "columns", Parts: []compilerlock.IndexPartRecord{{Column: "note"}},
	})
	base.Catalog.Objects[0].ExclusionConstraints = append(base.Catalog.Objects[0].ExclusionConstraints, compilerlock.ExclusionConstraintRecord{
		Name: "users_exclusion", Elements: []compilerlock.ExclusionElementRecord{{ExpressionSQL: "id", Operator: "="}},
	}, compilerlock.ExclusionConstraintRecord{
		Name: "users_note_exclusion", Elements: []compilerlock.ExclusionElementRecord{{ExpressionSQL: "note", Operator: "="}},
	})
	for i := range base.Queries {
		if base.Queries[i].ID == "q-unknown-postgresql" {
			base.Queries[i].Evidence.Diagnostics = append(base.Queries[i].Evidence.Diagnostics, "query_column_unknown")
		}
	}
	want, err := compilerlock.Encode(base)
	require.NoError(t, err)
	for seed := int64(1); seed <= 100; seed++ {
		encoded, err := compilerlock.Encode(base)
		require.NoError(t, err)
		f, err := compilerlock.Decode(encoded)
		require.NoError(t, err)
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
		for i := range f.Catalog.Objects {
			object := &f.Catalog.Objects[i]
			shuffle(len(object.Columns), func(i, j int) { object.Columns[i], object.Columns[j] = object.Columns[j], object.Columns[i] })
			shuffle(len(object.Constraints), func(i, j int) {
				object.Constraints[i], object.Constraints[j] = object.Constraints[j], object.Constraints[i]
			})
			shuffle(len(object.Indexes), func(i, j int) { object.Indexes[i], object.Indexes[j] = object.Indexes[j], object.Indexes[i] })
			shuffle(len(object.ExclusionConstraints), func(i, j int) {
				object.ExclusionConstraints[i], object.ExclusionConstraints[j] = object.ExclusionConstraints[j], object.ExclusionConstraints[i]
			})
		}
		shuffle(len(f.Queries), func(i, j int) { f.Queries[i], f.Queries[j] = f.Queries[j], f.Queries[i] })
		for qi := range f.Queries {
			shuffle(len(f.Queries[qi].Evidence.Diagnostics), func(i, j int) {
				f.Queries[qi].Evidence.Diagnostics[i], f.Queries[qi].Evidence.Diagnostics[j] = f.Queries[qi].Evidence.Diagnostics[j], f.Queries[qi].Evidence.Diagnostics[i]
			})
		}
		shuffle(len(f.Generation.Objects), func(i, j int) {
			f.Generation.Objects[i], f.Generation.Objects[j] = f.Generation.Objects[j], f.Generation.Objects[i]
		})
		shuffle(len(f.Generation.Queries), func(i, j int) {
			f.Generation.Queries[i], f.Generation.Queries[j] = f.Generation.Queries[j], f.Generation.Queries[i]
		})
		beforeColumns := make(map[string][]compilerlock.ColumnRecord, len(f.Catalog.Objects))
		for _, object := range f.Catalog.Objects {
			beforeColumns[object.ID] = append([]compilerlock.ColumnRecord(nil), object.Columns...)
		}
		beforeParameters := make(map[string][]compilerlock.ValueRecord, len(f.Queries))
		beforeResults := make(map[string][]compilerlock.ValueRecord, len(f.Queries))
		beforeKeys := make(map[string][]compilerlock.IndexPartRecord)
		for _, query := range f.Queries {
			beforeParameters[string(query.ID)] = append([]compilerlock.ValueRecord(nil), query.Parameters...)
			beforeResults[string(query.ID)] = append([]compilerlock.ValueRecord(nil), query.Results...)
		}
		for _, object := range f.Catalog.Objects {
			for _, index := range object.Indexes {
				beforeKeys[index.Name] = append([]compilerlock.IndexPartRecord(nil), index.Parts...)
			}
		}
		got, err := compilerlock.Encode(f)
		require.NoError(t, err)
		require.Equal(t, want, got, "seed %d", seed)
		for _, object := range f.Catalog.Objects {
			require.True(t, reflect.DeepEqual(beforeColumns[object.ID], object.Columns), "Encode must not mutate columns")
			for _, index := range object.Indexes {
				require.True(t, reflect.DeepEqual(beforeKeys[index.Name], index.Parts), "Encode must not mutate index key parts")
			}
		}
		for _, query := range f.Queries {
			require.True(t, reflect.DeepEqual(beforeParameters[string(query.ID)], query.Parameters), "Encode must not mutate parameters")
			require.True(t, reflect.DeepEqual(beforeResults[string(query.ID)], query.Results), "Encode must not mutate results")
		}
	}
}
