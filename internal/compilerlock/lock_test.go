package compilerlock_test

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

func TestPhysicalWireRoundTripFixtures(t *testing.T) {
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "compilerir", "testdata", dialect, "catalog.json"))
			if err != nil {
				t.Fatal(err)
			}
			var in struct {
				Engine  compilerir.EngineIdentity   `json:"engine"`
				Objects []compilerir.PhysicalObject `json:"objects"`
			}
			if err := json.Unmarshal(b, &in); err != nil {
				t.Fatal(err)
			}
			c := compilerir.PhysicalCatalog{Engine: in.Engine, Objects: in.Objects}
			f := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "test", Engine: compilerlock.EngineRecord{Dialect: in.Engine.Dialect, Version: in.Engine.Version, Profile: in.Engine.Profile}, Source: compilerlock.SourceRecord{Kind: "external", Identity: "fixture"}, Catalog: compilerlock.FromPhysical(c), Generation: compilerlock.GenerationRecord{Package: "p", Output: "o", Emitter: "compact"}, Digests: compilerlock.Digests{Source: strings.Repeat("a", 64), Mappings: strings.Repeat("b", 64), Queries: strings.Repeat("c", 64), Generation: strings.Repeat("d", 64)}}
			encoded, err := compilerlock.Encode(f)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := compilerlock.Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			reencoded, err := compilerlock.Encode(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != string(reencoded) {
				t.Fatal("encode/decode/encode changed canonical bytes")
			}
			want := c.Clone()
			for i := range want.Objects {
				sort.Slice(want.Objects[i].Constraints, func(a, b int) bool { return want.Objects[i].Constraints[a].Name < want.Objects[i].Constraints[b].Name })
				if len(want.Objects[i].Indexes) == 0 {
					want.Objects[i].Indexes = nil
				}
			}
			require.Equal(t, want, compilerlock.ToPhysical(decoded))
		})
	}
}

func TestDecodeRejectsMalformedPathsHashesAndDuplicateIDs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "v2", "sqlite.json"))
	require.NoError(t, err)
	cases := map[string]string{
		"path":    `"path": "queries/find.sql"`,
		"hash":    `"sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"`,
		"queryID": `"id": "q-known-sqlite"`,
	}
	for name, needle := range cases {
		t.Run(name, func(t *testing.T) {
			mutated := string(b)
			switch name {
			case "path":
				mutated = strings.Replace(mutated, needle, `"path": "../find.sql"`, 1)
			case "hash":
				mutated = strings.Replace(mutated, needle, `"sha256": "BAD"`, 1)
			case "queryID":
				mutated = strings.Replace(mutated, needle, `"id": "q-unknown-sqlite"`, 1)
			}
			_, err := compilerlock.Decode([]byte(mutated))
			require.Error(t, err)
		})
	}
}

func TestBuildDigestsKeepsInputGroupsIndependent(t *testing.T) {
	base := compilerlock.DigestInputs{
		Source:     compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "migrations", Identity: "schema", Files: []compilerlock.SourceFile{{Path: "001.sql", SHA256: strings.Repeat("a", 64)}, {Path: "002.sql", SHA256: strings.Repeat("b", 64)}}}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}, Materializer: []compilerlock.KeyValue{{Key: "z", Value: "2"}, {Key: "a", Value: "1"}}},
		Mappings:   compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "custom", GoType: "string", Imports: []compilerir.GoImport{{Path: "z/pkg", Alias: "z"}, {Path: "a/pkg", Alias: "a"}}}, {Name: "other", GoType: "int"}}},
		Queries:    []compilerlock.QueryDigestInput{{ID: "q", SQL: compilerlock.SourceFile{Path: "q.sql", SHA256: strings.Repeat("b", 64)}, Operation: "select", Parameters: []compilerlock.ValueRecord{{Name: "p", Scalar: "text", TypeCertainty: "known", NullabilityCertainty: "known"}}, Cardinality: "one"}},
		Generation: compilerir.GoConfig{Package: "store", Output: "gen", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "o", Source: "Users", Row: "UserRow", File: "users.go"}}, Queries: []compilerir.QueryGoName{{ID: "q", Function: "Find", Result: "FindResult", Projection: "FindProjection", Decoder: "decodeFind", File: "q.go"}}},
	}
	want, err := compilerlock.BuildDigests(base)
	require.NoError(t, err)
	mutations := []struct {
		name   string
		group  string
		mutate func(*compilerlock.DigestInputs)
	}{
		{"migration file hash", "source", func(in *compilerlock.DigestInputs) { in.Source.Record.Files[0].SHA256 = strings.Repeat("f", 64) }},
		{"materializer", "source", func(in *compilerlock.DigestInputs) { in.Source.Materializer[0].Value = "changed" }},
		{"mapping", "mappings", func(in *compilerlock.DigestInputs) { in.Mappings.Scalars[0].GoType = "int" }},
		{"mapping import", "mappings", func(in *compilerlock.DigestInputs) { in.Mappings.Scalars[0].Imports[0].Alias = "changed" }},
		{"sql", "queries", func(in *compilerlock.DigestInputs) { in.Queries[0].SQL.SHA256 = strings.Repeat("f", 64) }},
		{"cardinality", "queries", func(in *compilerlock.DigestInputs) { in.Queries[0].Cardinality = "many" }},
		{"package", "generation", func(in *compilerlock.DigestInputs) { in.Generation.Package = "other" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			in := cloneDigestInputs(base)
			tc.mutate(&in)
			got, err := compilerlock.BuildDigests(in)
			require.NoError(t, err)
			require.NotEqual(t, digestGroup(want, tc.group), digestGroup(got, tc.group))
			for _, group := range []string{"source", "mappings", "queries", "generation"} {
				if group != tc.group {
					require.Equal(t, digestGroup(want, group), digestGroup(got, group), "unrelated digest group %s changed", group)
				}
			}
		})
	}
}

func cloneDigestInputs(in compilerlock.DigestInputs) compilerlock.DigestInputs {
	out := in
	out.Source.Record.Files = append([]compilerlock.SourceFile(nil), in.Source.Record.Files...)
	out.Source.Materializer = append([]compilerlock.KeyValue(nil), in.Source.Materializer...)
	out.Mappings.Scalars = append([]compilerir.ScalarMapping(nil), in.Mappings.Scalars...)
	for i := range out.Mappings.Scalars {
		out.Mappings.Scalars[i].Imports = append([]compilerir.GoImport(nil), in.Mappings.Scalars[i].Imports...)
	}
	out.Queries = append([]compilerlock.QueryDigestInput(nil), in.Queries...)
	for i := range out.Queries {
		out.Queries[i].Parameters = append([]compilerlock.ValueRecord(nil), in.Queries[i].Parameters...)
		out.Queries[i].Results = append([]compilerlock.ValueRecord(nil), in.Queries[i].Results...)
	}
	out.Generation.Objects = append([]compilerir.ObjectGoName(nil), in.Generation.Objects...)
	out.Generation.Queries = append([]compilerir.QueryGoName(nil), in.Generation.Queries...)
	return out
}

func digestGroup(d compilerlock.Digests, group string) string {
	switch group {
	case "source":
		return d.Source
	case "mappings":
		return d.Mappings
	case "queries":
		return d.Queries
	case "generation":
		return d.Generation
	default:
		panic("unknown digest group")
	}
}

func TestBuildDigestsStableAcrossUnorderedInputs(t *testing.T) {
	value := compilerlock.ValueRecord{Name: "p", Scalar: "text", TypeCertainty: "known", NullabilityCertainty: "known"}
	in := compilerlock.DigestInputs{
		Source: compilerlock.SourceDigestInput{
			Record: compilerlock.SourceRecord{Kind: "migrations", Identity: "schema", Files: []compilerlock.SourceFile{{Path: "001.sql", SHA256: strings.Repeat("a", 64)}, {Path: "002.sql", SHA256: strings.Repeat("b", 64)}}},
			Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}, Materializer: []compilerlock.KeyValue{{Key: "z", Value: "2"}, {Key: "a", Value: "1"}},
		},
		Mappings: compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "z", Imports: []compilerir.GoImport{{Path: "z", Alias: "z"}, {Path: "a", Alias: "a"}}}, {Name: "a"}}},
		Queries: []compilerlock.QueryDigestInput{
			{ID: "q2", SQL: compilerlock.SourceFile{Path: "q2.sql", SHA256: strings.Repeat("c", 64)}, Operation: "select", Parameters: []compilerlock.ValueRecord{value}, Cardinality: "one"},
			{ID: "q1", SQL: compilerlock.SourceFile{Path: "q1.sql", SHA256: strings.Repeat("d", 64)}, Operation: "select", Parameters: []compilerlock.ValueRecord{value}, Cardinality: "many"},
		},
		Generation: compilerir.GoConfig{Package: "store", Output: "gen", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "o2", Source: "B", Row: "BRow", File: "b.go"}, {ID: "o1", Source: "A", Row: "ARow", File: "a.go"}}, Queries: []compilerir.QueryGoName{{ID: "q2", Function: "B", Result: "BResult", Projection: "BProjection", Decoder: "decodeB", File: "bq.go"}, {ID: "q1", Function: "A", Result: "AResult", Projection: "AProjection", Decoder: "decodeA", File: "aq.go"}}},
	}
	want, err := compilerlock.BuildDigests(in)
	require.NoError(t, err)
	for seed := int64(1); seed <= 100; seed++ {
		candidate := cloneDigestInputs(in)
		r := rand.New(rand.NewSource(seed))
		shuffle := func(n int, swap func(int, int)) {
			for i := n - 1; i > 0; i-- {
				swap(i, r.Intn(i+1))
			}
		}
		shuffle(len(candidate.Source.Record.Files), func(i, j int) {
			candidate.Source.Record.Files[i], candidate.Source.Record.Files[j] = candidate.Source.Record.Files[j], candidate.Source.Record.Files[i]
		})
		shuffle(len(candidate.Source.Materializer), func(i, j int) {
			candidate.Source.Materializer[i], candidate.Source.Materializer[j] = candidate.Source.Materializer[j], candidate.Source.Materializer[i]
		})
		shuffle(len(candidate.Mappings.Scalars), func(i, j int) {
			candidate.Mappings.Scalars[i], candidate.Mappings.Scalars[j] = candidate.Mappings.Scalars[j], candidate.Mappings.Scalars[i]
		})
		for i := range candidate.Mappings.Scalars {
			shuffle(len(candidate.Mappings.Scalars[i].Imports), func(a, b int) {
				candidate.Mappings.Scalars[i].Imports[a], candidate.Mappings.Scalars[i].Imports[b] = candidate.Mappings.Scalars[i].Imports[b], candidate.Mappings.Scalars[i].Imports[a]
			})
		}
		shuffle(len(candidate.Queries), func(i, j int) {
			candidate.Queries[i], candidate.Queries[j] = candidate.Queries[j], candidate.Queries[i]
		})
		shuffle(len(candidate.Generation.Objects), func(i, j int) {
			candidate.Generation.Objects[i], candidate.Generation.Objects[j] = candidate.Generation.Objects[j], candidate.Generation.Objects[i]
		})
		shuffle(len(candidate.Generation.Queries), func(i, j int) {
			candidate.Generation.Queries[i], candidate.Generation.Queries[j] = candidate.Generation.Queries[j], candidate.Generation.Queries[i]
		})
		got, err := compilerlock.BuildDigests(candidate)
		require.NoError(t, err)
		require.Equal(t, want, got, "seed %d", seed)
	}
}

func TestDecodeRejectsUnknownAndTrailingJSON(t *testing.T) {
	base := `{"format":1,"compiler":"x","source":{"kind":"external","identity":"x"},"engine":{"dialect":"sqlite","profile":"sqlite-3"},"catalog":{"objects":[]},"queries":[],"generation":{"package":"p","output":"o","emitter":"compact","prune":false,"objects":[]},"digests":{"source":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mappings":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","queries":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","generation":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}`
	if _, err := compilerlock.Decode([]byte(base + `{"extra":true}`)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	if _, err := compilerlock.Decode([]byte(`{"format":1,"compiler":"x","unknown":1}`)); err == nil {
		t.Fatal("accepted unknown field")
	}
}

func TestDecodeFormatTwoRequiresMappingsObject(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "v2", "sqlite.json"))
	require.NoError(t, err)
	without := bytes.Replace(b, []byte(",\n  \"mappings\": {}\n"), []byte("\n"), 1)
	if _, err := compilerlock.Decode(without); err == nil {
		t.Fatal("format 2 accepted a lock without mappings")
	}
	null := bytes.Replace(b, []byte(`"mappings": {}`), []byte(`"mappings": null`), 1)
	if _, err := compilerlock.Decode(null); err == nil {
		t.Fatal("format 2 accepted null mappings")
	}
	decoded, err := compilerlock.Decode(b)
	require.NoError(t, err)
	reencoded, err := compilerlock.Encode(decoded)
	require.NoError(t, err)
	require.Equal(t, b, reencoded)
}

func TestDecodeAndUpgradeV1CanonicalEmptyMappings(t *testing.T) {
	source := compilerlock.SourceRecord{Kind: "external", Identity: "fixture"}
	engine := compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}
	generation := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact"}
	digests, err := compilerlock.BuildDigests(compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: source, Engine: engine}, Generation: generation})
	require.NoError(t, err)
	file := compilerlock.File{Format: 1, Compiler: "test", Source: source, Engine: engine, Digests: digests, Generation: compilerlock.GenerationRecord{Package: generation.Package, Output: generation.Output, Emitter: generation.Emitter}}
	encoded, err := json.Marshal(file)
	require.NoError(t, err)
	encoded = bytes.Replace(encoded, []byte(`,"mappings":{}`), nil, 1)
	decoded, err := compilerlock.Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, compilerlock.FormatVersion, decoded.Format)
	require.NotNil(t, decoded.Mappings.Scalars)
	require.NotNil(t, decoded.Mappings.Relations)
	upgraded, err := compilerlock.Upgrade(encoded)
	require.NoError(t, err)
	decodedUpgrade, err := compilerlock.Decode(upgraded)
	require.NoError(t, err)
	require.Equal(t, compilerlock.FormatVersion, decodedUpgrade.Format)
	require.Contains(t, string(upgraded), `"mappings": {}`)
}

func TestBuildDigestsStable(t *testing.T) {
	in := compilerlock.DigestInputs{Source: compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "migrations", Identity: "x", Files: []compilerlock.SourceFile{{Path: "b.sql", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {Path: "a.sql", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}, Materializer: []compilerlock.KeyValue{{Key: "z", Value: "2"}, {Key: "a", Value: "1"}}}, Generation: compilerir.GoConfig{Package: "p", Output: "o", Emitter: "compact"}}
	a, err := compilerlock.BuildDigests(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Source.Record.Files[0], in.Source.Record.Files[1] = in.Source.Record.Files[1], in.Source.Record.Files[0]
	in.Source.Materializer[0], in.Source.Materializer[1] = in.Source.Materializer[1], in.Source.Materializer[0]
	b, err := compilerlock.BuildDigests(in)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("digest changed after reorder: %#v %#v", a, b)
	}
}
