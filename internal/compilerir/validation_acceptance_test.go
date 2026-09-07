package compilerir_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestValidatePhysicalRejectsDiscardedIndexModifiers(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "t", Kind: "table", Name: "t", Indexes: []compilerir.PhysicalIndex{{Name: "idx", KeyForm: "columns", Parts: []compilerir.IndexPart{{Column: "id", Direction: "DESC"}}}}}}}
	if err := compilerir.ValidatePhysical(catalog); err == nil || !strings.Contains(err.Error(), "parts") {
		t.Fatalf("expected exact index parts error, got %v", err)
	}
}

func TestValidateSemanticRejectsUnknownCardinality(t *testing.T) {
	err := compilerir.ValidateSemantic(compilerir.SemanticModel{Queries: []compilerir.SemanticQuery{{ID: "q", Name: "Find", Cardinality: "bogus"}}})
	if err == nil || !strings.Contains(err.Error(), "cardinality") {
		t.Fatalf("expected cardinality error, got %v", err)
	}
}

func TestBuildGoCreatesUsableDefaultShapesAndImports(t *testing.T) {
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{{ID: "t", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "Events"}, Columns: []compilerir.SemanticColumn{{Name: "when", Scalar: "time", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown}}}}}
	got, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store"})
	if len(diagnostics) != 0 || got.Objects[0].SourceName != "Events" || got.Objects[0].Create == nil || got.Objects[0].Patch == nil || got.Objects[0].Row.DecoderName == "" || len(got.Imports) != 2 {
		t.Fatalf("unexpected Go model: %#v diagnostics=%#v", got, diagnostics)
	}
}
