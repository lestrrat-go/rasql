package compilerlock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func BuildDigests(in DigestInputs) (Digests, error) {
	if in.Source.Record.Identity == "" || in.Source.Engine.Profile == "" {
		return Digests{}, fmt.Errorf("compilerlock: source identity and engine profile are required")
	}
	if in.Source.Record.Kind != "migrations" && in.Source.Record.Kind != "external" && in.Source.Record.Kind != "live" {
		return Digests{}, fmt.Errorf("compilerlock: unsupported source kind %q", in.Source.Record.Kind)
	}
	if err := validateSource(in.Source.Record); err != nil {
		return Digests{}, err
	}
	s := in.Source.Record
	s.Files = append([]SourceFile(nil), s.Files...)
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
	m := append([]KeyValue(nil), in.Source.Materializer...)
	seenKV := map[string]struct{}{}
	for _, v := range m {
		if v.Key == "" {
			return Digests{}, fmt.Errorf("compilerlock: empty materializer key")
		}
		if _, ok := seenKV[v.Key]; ok {
			return Digests{}, fmt.Errorf("compilerlock: duplicate materializer key %q", v.Key)
		}
		seenKV[v.Key] = struct{}{}
	}
	sort.Slice(m, func(i, j int) bool { return m[i].Key < m[j].Key })
	source := struct {
		Record       SourceRecord `json:"record"`
		Engine       EngineRecord `json:"engine"`
		Materializer []KeyValue   `json:"materializer"`
	}{s, in.Source.Engine, m}
	q := append([]QueryDigestInput(nil), in.Queries...)
	seenQ := map[string]struct{}{}
	sort.Slice(q, func(i, j int) bool { return q[i].ID < q[j].ID })
	for i := range q {
		if q[i].ID == "" {
			return Digests{}, fmt.Errorf("compilerlock: empty query ID")
		}
		if _, ok := seenQ[q[i].ID]; ok {
			return Digests{}, fmt.Errorf("compilerlock: duplicate query ID %q", q[i].ID)
		}
		seenQ[q[i].ID] = struct{}{}
		if !validOperation(q[i].Operation) {
			return Digests{}, fmt.Errorf("compilerlock: invalid cardinality %q", q[i].Cardinality)
		}
		if err := validateValues(q[i].Parameters); err != nil {
			return Digests{}, err
		}
		if err := validateValues(q[i].Results); err != nil {
			return Digests{}, err
		}
		if err := validateSource(SourceRecord{Files: []SourceFile{q[i].SQL}}); err != nil {
			return Digests{}, err
		}
	}
	g := in.Generation.Clone()
	if err := validateGoGeneration(g); err != nil {
		return Digests{}, err
	}
	sort.Slice(g.Objects, func(i, j int) bool { return g.Objects[i].ID < g.Objects[j].ID })
	sort.Slice(g.Queries, func(i, j int) bool { return g.Queries[i].ID < g.Queries[j].ID })
	gen := struct {
		Package, Output, Emitter string
		Prune                    bool
		Objects                  []compilerir.ObjectGoName
		Queries                  []compilerir.QueryGoName
	}{g.Package, g.Output, g.Emitter, g.Prune, g.Objects, g.Queries}
	return Digests{Source: hash(source), Mappings: hash(normalizedMappings(in.Mappings)), Queries: hash(q), Generation: hash(gen)}, nil
}
func validateGoGeneration(g compilerir.GoConfig) error {
	r := GenerationRecord{Package: g.Package, Output: g.Output, Emitter: g.Emitter, Prune: g.Prune}
	c := CatalogRecord{}
	qs := []QueryRecord{}
	for _, o := range g.Objects {
		r.Objects = append(r.Objects, ObjectNameRecord{ID: string(o.ID), Source: o.Source, Row: o.Row, Create: o.Create, Patch: o.Patch, File: o.File})
		c.Objects = append(c.Objects, ObjectRecord{ID: string(o.ID)})
	}
	for _, q := range g.Queries {
		r.Queries = append(r.Queries, QueryNameRecord{ID: string(q.ID), Function: q.Function, Result: q.Result, Projection: q.Projection, Decoder: q.Decoder, File: q.File})
		qs = append(qs, QueryRecord{ID: q.ID})
	}
	return validateGeneration(r, c, qs)
}
func normalizedMappings(m compilerir.MappingConfig) compilerir.MappingConfig {
	m = m.Clone()
	sort.Slice(m.Scalars, func(i, j int) bool { return m.Scalars[i].Name < m.Scalars[j].Name })
	sort.Slice(m.Relations, func(i, j int) bool {
		a, b := m.Relations[i], m.Relations[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Through.Object < b.Through.Object
	})
	for i := range m.Scalars {
		sort.Slice(m.Scalars[i].Imports, func(a, b int) bool {
			if m.Scalars[i].Imports[a].Path != m.Scalars[i].Imports[b].Path {
				return m.Scalars[i].Imports[a].Path < m.Scalars[i].Imports[b].Path
			}
			return m.Scalars[i].Imports[a].Alias < m.Scalars[i].Imports[b].Alias
		})
	}
	return m
}
func hash(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	_ = e.Encode(v)
	h := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(h[:])
}
func ValidateDigest(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("compilerlock: invalid digest")
	}
	for _, c := range s {
		if !isLowerHex(c) {
			return fmt.Errorf("compilerlock: invalid lowercase digest")
		}
	}
	return nil
}
func NormalizePath(p string) (string, error) {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || len(p) >= 3 && p[:3] == "../" || len(p) >= 2 && p[1] == ':' || bytes.Contains([]byte(p), []byte{'\\'}) {
		return "", fmt.Errorf("compilerlock: invalid relative path %q", p)
	}
	return p, nil
}
