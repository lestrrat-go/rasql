package generate_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// compactPackageStore is compact_fixture_test.go's compactStore for tests
// that live in this external generate_test package rather than inside
// package generate itself: it drives the same compiler pipeline through
// generate's own exported surface (NewEmitterInput and RenderCompact) since
// this package cannot reach generate's unexported helpers directly.
func compactPackageStore(t *testing.T, dir string, tables ...schema.TableDef) generate.Store {
	t.Helper()
	engine := compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, tables)
	require.Empty(t, diagnostics)
	catalog, diagnostics = compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "compact-fixture"})
	require.Empty(t, diagnostics)
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	require.Empty(t, diagnostics)
	objects := make([]compilerir.ObjectGoName, len(catalog.Objects))
	for i, object := range catalog.Objects {
		objects[i] = compilerir.ObjectGoName{ID: object.ID, File: strings.ToLower(object.Name) + "_gen.go"}
	}
	config := compilerir.GoConfig{Package: "generated", Output: dir, Emitter: "compact", Objects: objects}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	require.Empty(t, diagnostics)
	in, err := generate.NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	require.NoError(t, err)
	store, err := generate.RenderCompact(in)
	require.NoError(t, err)
	return store
}
