package schemasource

import (
	"context"
	"reflect"
	"sort"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
)

func (r Result) Clone() Result {
	x := r
	x.Catalog = r.Catalog.Clone()
	x.Snapshots = append([]sourcefile.SourceFileSnapshot(nil), r.Snapshots...)
	x.Unresolved = append([]catalogread.UnresolvedFact(nil), r.Unresolved...)
	x.Queries = append([]compilerir.QueryAnalysis(nil), r.Queries...)
	for i := range x.Queries {
		x.Queries[i] = r.Queries[i].Clone()
	}
	x.Source.Record.Files = append([]compilerlock.SourceFile(nil), r.Source.Record.Files...)
	x.Source.Materializer = append([]compilerlock.KeyValue(nil), r.Source.Materializer...)
	return x
}
func (r VerifyResult) Clone() VerifyResult {
	x := r
	x.Candidate = r.Candidate.Clone()
	x.Differences = append([]string(nil), r.Differences...)
	return x
}
func Verify(ctx context.Context, req Request, deps Dependencies, expected compilerlock.File) (VerifyResult, error) {
	candidate, err := Materialize(ctx, req, deps)
	if err != nil {
		return VerifyResult{}, err
	}
	diffs := make([]string, 0)
	if candidate.Source.Record.Kind != expected.Source.Kind || candidate.Source.Record.Identity != expected.Source.Identity || !sameFiles(candidate.Source.Record.Files, expected.Source.Files) {
		diffs = append(diffs, "source")
	}
	if candidate.Profile.ID != expected.Engine.Profile || engineName(candidate.Profile.Engine) != expected.Engine.Dialect || versionString(candidate.Profile) != expected.Engine.Version {
		diffs = append(diffs, "engine")
	}
	if !catalogEqual(candidate.Catalog, compilerlock.PhysicalFromCatalog(expected)) {
		diffs = append(diffs, "catalog")
	}
	sort.Strings(diffs)
	return VerifyResult{Candidate: candidate, Differences: unique(diffs)}, nil
}
func catalogEqual(a, b compilerir.PhysicalCatalog) bool {
	return reflect.DeepEqual(a, b)
}
func unique(in []string) []string {
	out := in[:0]
	for _, x := range in {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}
func sameFiles(a, b []compilerlock.SourceFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func engineName(e engineprofile.EngineID) string {
	switch e {
	case engineprofile.PostgreSQL:
		return "postgresql"
	case engineprofile.MySQL:
		return "mysql"
	case engineprofile.SQLite:
		return "sqlite"
	}
	return ""
}
