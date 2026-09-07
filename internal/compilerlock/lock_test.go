package compilerlock_test

import (
	"encoding/json"
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
	b, err := os.ReadFile(filepath.Join("testdata", "v1", "sqlite.json"))
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
		Source:     compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "migrations", Identity: "schema", Files: []compilerlock.SourceFile{{Path: "001.sql", SHA256: strings.Repeat("a", 64)}}}, Engine: compilerlock.EngineRecord{Dialect: "sqlite", Profile: "sqlite-3"}},
		Mappings:   compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "custom", GoType: "string"}}},
		Queries:    []compilerlock.QueryDigestInput{{ID: "q", SQL: compilerlock.SourceFile{Path: "q.sql", SHA256: strings.Repeat("b", 64)}, Operation: "select", Parameters: []compilerlock.ValueRecord{{Name: "p", Scalar: "text", TypeCertainty: "known", NullabilityCertainty: "known"}}, Cardinality: "one"}},
		Generation: compilerir.GoConfig{Package: "store", Output: "gen", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "o", Source: "Users", Row: "UserRow", File: "users.go"}}, Queries: []compilerir.QueryGoName{{ID: "q", Function: "Find", Result: "FindResult", Projection: "FindProjection", Decoder: "decodeFind", File: "q.go"}}},
	}
	want, err := compilerlock.BuildDigests(base)
	require.NoError(t, err)
	mutations := []struct {
		name   string
		mutate func(*compilerlock.DigestInputs)
		pick   func(compilerlock.Digests) string
	}{
		{"source", func(in *compilerlock.DigestInputs) { in.Source.Record.Identity = "other" }, func(d compilerlock.Digests) string { return d.Source }},
		{"mappings", func(in *compilerlock.DigestInputs) { in.Mappings.Scalars[0].GoType = "int" }, func(d compilerlock.Digests) string { return d.Mappings }},
		{"sql", func(in *compilerlock.DigestInputs) { in.Queries[0].SQL.SHA256 = strings.Repeat("f", 64) }, func(d compilerlock.Digests) string { return d.Queries }},
		{"cardinality", func(in *compilerlock.DigestInputs) { in.Queries[0].Cardinality = "many" }, func(d compilerlock.Digests) string { return d.Queries }},
		{"generation", func(in *compilerlock.DigestInputs) { in.Generation.Package = "other" }, func(d compilerlock.Digests) string { return d.Generation }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Source.Record.Files = append([]compilerlock.SourceFile(nil), base.Source.Record.Files...)
			in.Mappings.Scalars = append([]compilerir.ScalarMapping(nil), base.Mappings.Scalars...)
			in.Queries = append([]compilerlock.QueryDigestInput(nil), base.Queries...)
			in.Generation.Objects = append([]compilerir.ObjectGoName(nil), base.Generation.Objects...)
			in.Generation.Queries = append([]compilerir.QueryGoName(nil), base.Generation.Queries...)
			tc.mutate(&in)
			got, err := compilerlock.BuildDigests(in)
			require.NoError(t, err)
			require.NotEqual(t, tc.pick(want), tc.pick(got))
		})
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
