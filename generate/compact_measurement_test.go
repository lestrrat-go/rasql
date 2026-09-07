package generate_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/stretchr/testify/require"
)

func TestCompactGeneratedMeasurements(t *testing.T) {
	in := measurementEmitterFixture(t)
	legacyInput := in.Clone()
	legacyInput.Generation.Emitter = "legacy"
	legacy, err := generate.LegacyStore(legacyInput)
	require.NoError(t, err)
	compactInput := in.Clone()
	compactInput.Generation.Emitter = "compact"
	compact, err := generate.RenderCompact(compactInput)
	require.NoError(t, err)

	legacy.Root, legacy.Dir = t.TempDir(), "generated"
	legacyPlan, err := legacy.Plan()
	require.NoError(t, err)
	compact.Root, compact.Dir = t.TempDir(), "generated"
	compactPlan, err := compact.Plan()
	require.NoError(t, err)
	legacyMetrics := generatedMetrics(t, legacyPlan.Files())
	compactMetrics := generatedMetrics(t, compactPlan.Files())
	t.Logf("legacy declarations=%d lines=%d bytes=%d", legacyMetrics.declarations, legacyMetrics.lines, legacyMetrics.bytes)
	t.Logf("compact declarations=%d lines=%d bytes=%d", compactMetrics.declarations, compactMetrics.lines, compactMetrics.bytes)
	// These unlike public surfaces remain recorded as evidence. The release
	// thresholds live in contracts/generation.md under Generated size gates.
	t.Logf("legacy-to-compact declarations ratio=%0.2f lines ratio=%0.2f", float64(compactMetrics.declarations)/float64(legacyMetrics.declarations), float64(compactMetrics.lines)/float64(legacyMetrics.lines))
}

func measurementEmitterFixture(t *testing.T) generate.EmitterInput {
	t.Helper()
	integer := func(name string) compilerir.PhysicalColumn {
		return compilerir.PhysicalColumn{Name: name, LogicalKind: "integer"}
	}
	users := compilerir.PhysicalObject{ID: "users", Kind: "table", Name: "users"}
	users.Columns = []compilerir.PhysicalColumn{integer("id"), {Name: "name", Ordinal: 1, LogicalKind: "text"}}
	for i := 0; i < 8; i++ {
		users.Columns = append(users.Columns, compilerir.PhysicalColumn{Name: fmt.Sprintf("field_%d", i), Ordinal: len(users.Columns), LogicalKind: "text"})
	}
	users.Constraints = []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}
	projects := compilerir.PhysicalObject{ID: "projects", Kind: "table", Name: "projects"}
	projects.Columns = []compilerir.PhysicalColumn{integer("id"), {Name: "owner_id", Ordinal: 1, LogicalKind: "integer"}}
	for i := 0; i < 8; i++ {
		projects.Columns = append(projects.Columns, compilerir.PhysicalColumn{Name: fmt.Sprintf("field_%d", i), Ordinal: len(projects.Columns), LogicalKind: "text"})
	}
	projects.Constraints = []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "foreign_key", Name: "projects_owner_fk", Columns: []string{"owner_id"}, Reference: &compilerir.ForeignReference{Object: "users", Columns: []string{"id"}}}}
	tasks := compilerir.PhysicalObject{ID: "tasks", Kind: "table", Name: "tasks"}
	tasks.Columns = []compilerir.PhysicalColumn{integer("id"), {Name: "project_id", Ordinal: 1, LogicalKind: "integer"}, {Name: "title", Ordinal: 2, LogicalKind: "text"}}
	for i := 0; i < 8; i++ {
		tasks.Columns = append(tasks.Columns, compilerir.PhysicalColumn{Name: fmt.Sprintf("field_%d", i), Ordinal: len(tasks.Columns), LogicalKind: "text"})
	}
	tasks.Constraints = []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}, {Kind: "foreign_key", Name: "tasks_project_fk", Columns: []string{"project_id"}, Reference: &compilerir.ForeignReference{Object: "projects", Columns: []string{"id"}}}}
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{users, projects, tasks}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "legacy", Objects: []compilerir.ObjectGoName{
		{ID: "users", File: "users_gen.go"},
		{ID: "projects", File: "projects_gen.go"},
		{ID: "tasks", File: "tasks_gen.go"},
	}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	return in
}

type generatedMetric struct {
	declarations int
	lines        int
	bytes        int
}

func generatedMetrics(t *testing.T, files []generate.File) generatedMetric {
	t.Helper()
	var result generatedMetric
	for _, file := range files {
		if strings.HasSuffix(file.Path, "_test.go") {
			continue
		}
		fileLines := 0
		fileDeclarations := 0
		result.bytes += len(file.Source)
		for _, line := range strings.Split(string(file.Source), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
				result.lines++
				fileLines++
			}
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Source, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				result.declarations++
				fileDeclarations++
			case *ast.GenDecl:
				result.declarations += len(declaration.Specs)
				fileDeclarations += len(declaration.Specs)
			}
		}
		t.Logf("%s declarations=%d lines=%d bytes=%d", filepath.Base(file.Path), fileDeclarations, fileLines, len(file.Source))
	}
	return result
}
