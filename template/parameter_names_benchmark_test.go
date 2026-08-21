package template_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/template"
)

// BenchmarkCompiledParameterNames measures ranging Compiled.ParameterNames
// over a two-name template. It returns slices.Values(c.uniqueNames) rather
// than a copied slice, so ranging it should cost 0 allocations; a regression
// back to a copying implementation shows up here as a jump from 0 to 1
// alloc/op.
func BenchmarkCompiledParameterNames(b *testing.B) {
	parsed, err := template.Parse("user_by_email", "SELECT id FROM users WHERE email = {{bind \"email\"}} OR backup_email = {{ bind \"email\" }} AND active = {{bind \"active\"}}")
	if err != nil {
		b.Fatal(err)
	}
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		count := 0
		for range compiled.ParameterNames() {
			count++
		}
		if count != 2 {
			b.Fatalf("got %d parameter names, want 2", count)
		}
	}
}
