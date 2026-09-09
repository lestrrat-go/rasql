package generate

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// compactStore builds the Store the commit tests plan and write from. Every
// test in this package used to construct one directly from table definitions,
// which the emitter split made impossible: a Store now carries what an emitter
// rendered, and the only emitter is compact. Going through RenderCompact here
// keeps these tests planning the same files rasqlgen really writes, rather
// than files a test assembled to look like them.
//
// It stays in this package rather than beside generate_test's own emitter
// fixtures, for the reason the file comment in store_commit_test.go gives:
// these tests reach unexported seams, so they cannot move out.
func compactStore(t *testing.T, dir string, tables ...schema.TableDef) Store {
	t.Helper()
	store, err := newCompactStore(dir, tables...)
	require.NoError(t, err)
	return store
}

// pruningStore is compactStore with Prune set, which enough commit tests want
// that spelling it out at each one would bury what they are actually checking.
func pruningStore(t *testing.T, dir string, tables ...schema.TableDef) Store {
	t.Helper()
	store := compactStore(t, dir, tables...)
	store.Prune = true
	return store
}

// publicationTestQuery is the second generated file the publication tests need
// beside a table's own, so that what they check about ordering and rollback is
// checked over more than one file.
func publicationTestQuery() TypedQuery {
	return TypedQuery{
		Function: "Q", Output: "q_gen.go", Engine: "sqlite", SQL: "SELECT id FROM users",
		Operation: "select", Cardinality: "many", Result: "QResult", Decoder: "QDecoder",
		Results: []querygen.TypedValue{
			{Go: compilerir.GoField{Name: "id", Type: "int64"}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "integer"}},
		},
	}
}

// newCompactStore reports its error instead of failing the test, for the two
// callers that plan the same tables twice and need the second attempt's error.
func newCompactStore(dir string, tables ...schema.TableDef) (Store, error) {
	engine := compilerir.EngineIdentity{Dialect: "sqlite", Version: "3"}
	catalog, diagnostics := compilerir.PhysicalFromTableDefs(engine, tables)
	if len(diagnostics) != 0 {
		return Store{}, diagnosticError("physical", diagnostics)
	}
	catalog, diagnostics = compilerir.AssignObjectIDs(catalog, compilerir.IdentityInput{SourceIdentity: "compact-fixture"})
	if len(diagnostics) != 0 {
		return Store{}, diagnosticError("identity", diagnostics)
	}
	semantic, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 {
		return Store{}, diagnosticError("semantic", diagnostics)
	}
	objects := make([]compilerir.ObjectGoName, len(catalog.Objects))
	for i, object := range catalog.Objects {
		// Lowercased to match filenameKey's documented invariant in store.go:
		// a table's generated file name is always already lowercase, so only
		// Query.Output needs folding there.
		objects[i] = compilerir.ObjectGoName{ID: object.ID, File: strings.ToLower(object.Name) + "_gen.go"}
	}
	config := compilerir.GoConfig{Package: "store", Output: dir, Emitter: "compact", Objects: objects}
	model, diagnostics := compilerir.BuildGo(semantic, config)
	if len(diagnostics) != 0 {
		return Store{}, diagnosticError("go", diagnostics)
	}
	in, err := NewEmitterInput(catalog, semantic, model, config, compilerir.MappingConfig{})
	if err != nil {
		return Store{}, err
	}
	return RenderCompact(in)
}

func diagnosticError(stage string, diagnostics []compilerir.Diagnostic) error {
	return &compactFixtureError{stage: stage, diagnostics: diagnostics}
}

type compactFixtureError struct {
	stage       string
	diagnostics []compilerir.Diagnostic
}

func (e *compactFixtureError) Error() string {
	return "compact fixture: " + e.stage + " diagnostics: " + e.diagnostics[0].Message
}
