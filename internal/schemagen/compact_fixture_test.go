package schemagen_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// compactStore builds tables through the compact emitter -- the only
// emitter rasqlgen still supports -- targeted at dir. It mirrors
// generate_test's own compactPackageStore, duplicated here because this
// package cannot reach generate's unexported test helpers, and because a
// test here is checking schemagen's own compact renderer rather than
// generate.Store.
func compactStore(t *testing.T, dir string, tables ...schema.TableDef) generate.Store {
	t.Helper()
	engine := compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, tables)
	require.Empty(t, diagnostics)
	catalog, diagnostics = compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "schemagen-fixture"})
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

// compactRenderedSource plans tables into a scratch directory and returns
// every planned file's source concatenated, so a test can assert on
// rendered Go text without caring which file a declaration landed in.
func compactRenderedSource(t *testing.T, tables ...schema.TableDef) string {
	t.Helper()
	plan, err := compactStore(t, t.TempDir(), tables...).Plan()
	require.NoError(t, err)
	var source strings.Builder
	for _, file := range plan.Files() {
		source.Write(file.Source)
		source.WriteByte('\n')
	}
	return source.String()
}
