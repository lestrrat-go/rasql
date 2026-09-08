package inspect_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// compactPlan plans an inspected table (or tables) through the compact
// emitter -- the only emitter rasqlgen still supports. It mirrors
// generate_test's own compactPackageStore, duplicated here because this
// package cannot reach generate's unexported test helpers.
func compactPlan(t *testing.T, tables ...schema.TableDef) generate.Plan {
	t.Helper()
	engine := compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, tables)
	require.Empty(t, diagnostics)
	catalog, diagnostics = compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "inspect-fixture"})
	require.Empty(t, diagnostics)
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	require.Empty(t, diagnostics)
	objects := make([]compilerir.ObjectGoName, len(catalog.Objects))
	for i, object := range catalog.Objects {
		objects[i] = compilerir.ObjectGoName{ID: object.ID, File: strings.ToLower(object.Name) + "_gen.go"}
	}
	config := compilerir.GoConfig{Package: "generated", Output: t.TempDir(), Emitter: "compact", Objects: objects}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	plan, err := store.Plan()
	require.NoError(t, err)
	return plan
}

// compactRenderedSource returns every file compactPlan plans, concatenated,
// for a test that asserts on rendered Go text without caring which file a
// declaration landed in.
func compactRenderedSource(t *testing.T, tables ...schema.TableDef) string {
	t.Helper()
	var source strings.Builder
	for _, file := range compactPlan(t, tables...).Files() {
		source.Write(file.Source)
		source.WriteByte('\n')
	}
	return source.String()
}

// compactDescriptorLiteral renders table's schema.TableDef literal the same
// way the compact emitter's own schema_gen.go does (compact.go's
// CompactObjectSource calls the same schemagen.TableDefinitionLiteral), as a
// standalone parseable Go file. Unlike compactFile it does not go through
// compilerir.BuildSemantic, so it stays usable for a table carrying an
// OpaqueType column with no configured scalar mapping -- BuildSemantic
// refuses those with "opaque native type requires an explicit mapping", a
// refusal about resolving a Go type for the column, not about whether the
// column's own descriptor is well-formed.
func compactDescriptorLiteral(t *testing.T, table schema.TableDef) []byte {
	t.Helper()
	literal, err := schemagen.TableDefinitionLiteral(table)
	require.NoError(t, err)
	return []byte("package descriptor\n\nimport \"github.com/lestrrat-go/rasql/schema\"\n\nvar _ = " + literal + "\n")
}
