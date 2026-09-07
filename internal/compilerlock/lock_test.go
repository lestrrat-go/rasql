package compilerlock_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
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
			if got := compilerlock.ToPhysical(decoded); got.Engine != c.Engine || len(got.Objects) != len(c.Objects) {
				t.Fatalf("physical round trip lost catalog: %#v", got)
			}
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
