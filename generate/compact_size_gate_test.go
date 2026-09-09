package generate_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"unicode"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/stretchr/testify/require"
)

func TestCompactGeneratedSizeGates(t *testing.T) {
	metrics := make(map[int]generatedSurfaceMetric, 3)
	for _, columns := range []int{3, 31, 100} {
		in := sizeGateEmitterFixture(t, columns)
		store, err := generate.RenderCompact(in)
		require.NoError(t, err)
		metrics[columns] = generatedSurfaceMetrics(t, store)
	}
	for _, columns := range []int{3, 31, 100} {
		metric := metrics[columns]
		t.Logf("compact columns=%d public=%d private=%d privateLines=%d privateBudget=%d imports=%d declarations=%d lines=%d bytes=%d", columns, metric.publicDeclarations, metric.privateDeclarations, metric.privateLines, 55+40+5*(columns+1), metric.imports, metric.declarations, metric.lines, metric.bytes)
		require.LessOrEqual(t, metric.privateDeclarations, 7+14, "private declarations for one object")
		require.LessOrEqual(t, metric.privateLines, 55+40+5*(columns+1), "private lines for one object")
	}
	require.LessOrEqual(t, metrics[31].lines-metrics[3].lines, 25*28+12)
	require.LessOrEqual(t, metrics[100].lines-metrics[31].lines, 25*69+12)
	require.Equal(t, 3*28, metrics[31].publicDeclarations-metrics[3].publicDeclarations)
	require.Equal(t, 3*69, metrics[100].publicDeclarations-metrics[31].publicDeclarations)

	// Public declarations are checked against the actual one-column delta.
	// One ordinary column contributes a page key, create setter, and patch
	// setter. The test below renders a pair so the expected delta is explicit.
	pair := make([]generatedSurfaceMetric, 2)
	for index, columns := range []int{3, 4} {
		store, err := generate.RenderCompact(sizeGateEmitterFixture(t, columns))
		require.NoError(t, err)
		pair[index] = generatedSurfaceMetrics(t, store)
	}
	require.Equal(t, 3, pair[1].publicDeclarations-pair[0].publicDeclarations)
}

func TestCompactPublicStateMethodGates(t *testing.T) {
	base := sizeGateEmitterFixture(t, 3)
	baseMetric := renderSurfaceMetric(t, base)
	nullable := sizeGateEmitterFixtureState(t, 3, true, false)
	nullableMetric := renderSurfaceMetric(t, nullable)
	require.Equal(t, 2, nullableMetric.publicDeclarations-baseMetric.publicDeclarations)
	defaulted := sizeGateEmitterFixtureState(t, 3, false, true)
	defaultedMetric := renderSurfaceMetric(t, defaulted)
	require.Equal(t, 2, defaultedMetric.publicDeclarations-baseMetric.publicDeclarations)
}

func renderSurfaceMetric(t *testing.T, input generate.EmitterInput) generatedSurfaceMetric {
	t.Helper()
	store, err := generate.RenderCompact(input)
	require.NoError(t, err)
	return generatedSurfaceMetrics(t, store)
}

type generatedSurfaceMetric struct {
	publicDeclarations  int
	privateDeclarations int
	privateLines        int
	imports             int
	declarations        int
	lines               int
	bytes               int
}

func generatedSurfaceMetrics(t *testing.T, store generate.Store) generatedSurfaceMetric {
	t.Helper()
	plan, err := store.Plan()
	require.NoError(t, err)
	var result generatedSurfaceMetric
	for _, file := range plan.Files() {
		if strings.HasSuffix(file.Path, "_test.go") {
			continue
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, file.Path, file.Source, 0)
		require.NoError(t, err)
		result.bytes += len(file.Source)
		sourceLines := strings.Split(string(file.Source), "\n")
		result.lines += nonCommentLines(string(file.Source))
		result.imports += len(parsed.Imports)
		privateLines := make(map[int]struct{})
		markPrivate := func(node ast.Node) {
			start := fileSet.Position(node.Pos()).Line
			end := fileSet.Position(node.End()).Line
			for line := start; line <= end; line++ {
				privateLines[line] = struct{}{}
			}
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				result.declarations++
				if isPublicFunc(declaration) {
					result.publicDeclarations++
				} else {
					result.privateDeclarations++
					markPrivate(declaration)
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					if declaration.Tok == token.IMPORT {
						continue
					}
					result.declarations++
					public := false
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						public = isExportedName(spec.Name.Name)
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							public = public || isExportedName(name.Name)
						}
					}
					if public {
						result.publicDeclarations++
					} else {
						result.privateDeclarations++
						t.Logf("private spec lines=%d", fileSet.Position(spec.End()).Line-fileSet.Position(spec.Pos()).Line+1)
						markPrivate(spec)
					}
				}
			}
		}
		for line := range privateLines {
			if line > 0 && line <= len(sourceLines) {
				trimmed := strings.TrimSpace(sourceLines[line-1])
				if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
					result.privateLines++
				}
			}
		}
	}
	return result
}

func nonCommentLines(source string) int {
	count := 0
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			count++
		}
	}
	return count
}

func isPublicFunc(function *ast.FuncDecl) bool {
	if !isExportedName(function.Name.Name) {
		return false
	}
	return function.Recv == nil || isExportedReceiver(function.Recv)
}

func isExportedReceiver(field *ast.FieldList) bool {
	if field == nil || len(field.List) != 1 {
		return false
	}
	typ := field.List[0].Type
	for {
		switch value := typ.(type) {
		case *ast.StarExpr:
			typ = value.X
		case *ast.IndexExpr:
			typ = value.X
		case *ast.IndexListExpr:
			typ = value.X
		case *ast.Ident:
			return isExportedName(value.Name)
		default:
			return false
		}
	}
}

func isExportedName(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}

func sizeGateEmitterFixture(t *testing.T, ordinaryColumns int) generate.EmitterInput {
	return sizeGateEmitterFixtureState(t, ordinaryColumns, false, false)
}

func sizeGateEmitterFixtureState(t *testing.T, ordinaryColumns int, nullable, defaulted bool) generate.EmitterInput {
	t.Helper()
	columns := []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}
	for index := 1; index <= ordinaryColumns; index++ {
		column := compilerir.PhysicalColumn{Name: fmt.Sprintf("field_%d", index), Ordinal: index, LogicalKind: "text"}
		if index == 1 {
			column.Nullable = nullable
			if defaulted {
				column.DefaultSQL = "'default'"
			}
		}
		columns = append(columns, column)
	}
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}, Objects: []compilerir.PhysicalObject{{ID: "users", Kind: "table", Name: "users", Columns: columns, Constraints: []compilerir.PhysicalConstraint{{Kind: "primary_key", Columns: []string{"id"}}}}}}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	config := compilerir.GoConfig{Package: "store", Output: "generated", Emitter: "compact", Objects: []compilerir.ObjectGoName{{ID: "users", File: "users_gen.go"}}}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	for _, diagnostic := range diagnostics {
		require.NotEqual(t, compilerir.DiagnosticError, diagnostic.Level, diagnostic.Message)
	}
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	return in
}
