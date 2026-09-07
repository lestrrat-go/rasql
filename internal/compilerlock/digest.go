package compilerlock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
)

func BuildDigests(in DigestInputs) (Digests, error) {
	if err := validateSource(in.Source.Record); err != nil {
		return Digests{}, err
	}
	s := in.Source.Record
	s.Files = append([]SourceFile(nil), s.Files...)
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
	m := append([]KeyValue(nil), in.Source.Materializer...)
	sort.Slice(m, func(i, j int) bool { return m[i].Key < m[j].Key })
	source := struct {
		Record       SourceRecord `json:"record"`
		Engine       EngineRecord `json:"engine"`
		Materializer []KeyValue   `json:"materializer"`
	}{s, in.Source.Engine, m}
	q := append([]QueryDigestInput(nil), in.Queries...)
	sort.Slice(q, func(i, j int) bool { return q[i].ID < q[j].ID })
	for i := range q {
		sort.Slice(q[i].Parameters, func(a, b int) bool { return q[i].Parameters[a].Name < q[i].Parameters[b].Name })
		sort.Slice(q[i].Results, func(a, b int) bool { return q[i].Results[a].Name < q[i].Results[b].Name })
		if err := validateSource(SourceRecord{Files: []SourceFile{q[i].SQL}}); err != nil {
			return Digests{}, err
		}
	}
	g := in.Generation.Clone()
	sort.Slice(g.Objects, func(i, j int) bool { return g.Objects[i].ID < g.Objects[j].ID })
	sort.Slice(g.Queries, func(i, j int) bool { return g.Queries[i].ID < g.Queries[j].ID })
	sort.Slice(g.Scalars, func(i, j int) bool { return g.Scalars[i].Name < g.Scalars[j].Name })
	return Digests{Source: hash(source), Mappings: hash(in.Mappings.Clone()), Queries: hash(q), Generation: hash(g)}, nil
}

func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ValidateDigest(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("compilerlock: invalid digest")
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("compilerlock: invalid digest: %w", err)
	}
	return nil
}
func NormalizePath(p string) (string, error) {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || len(p) >= 3 && p[:3] == "../" {
		return "", fmt.Errorf("compilerlock: invalid relative path %q", p)
	}
	return p, nil
}
