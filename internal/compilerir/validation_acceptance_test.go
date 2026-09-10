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

func TestValidatePhysicalRejectsMissingFactsAndInvalidEnums(t *testing.T) {
	base := compilerir.PhysicalObject{ID: "t", Kind: "table", Name: "t", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer"}}, Constraints: []compilerir.PhysicalConstraint{{Kind: "foreign_key", Columns: []string{"id"}}}}
	err := compilerir.ValidatePhysical(compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{base}})
	if err == nil || !strings.Contains(err.Error(), "reference") {
		t.Fatalf("expected missing reference error, got %v", err)
	}
	base.Constraints = nil
	base.Columns[0].Identity = "always"
	if err := compilerir.ValidatePhysical(compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{base}}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected identity error, got %v", err)
	}
}

func TestValidateGoRejectsNonTypesAndMissingImports(t *testing.T) {
	shape := &compilerir.GoShape{Name: "Create", Fields: []compilerir.GoField{{Name: "Value", Type: "1+2"}}}
	m := compilerir.GoModel{Package: "store", Objects: []compilerir.GoObject{{ID: "t", SourceName: "T", Row: compilerir.GoShape{Name: "TRow", DecoderName: "DecodeT"}, Create: shape, Patch: shape}}}
	if err := compilerir.ValidateGo(m); err == nil {
		t.Fatal("expected non-type error")
	}
	shape.Fields[0].Type = "missing.Type"
	if err := compilerir.ValidateGo(m); err == nil || !strings.Contains(err.Error(), "import") {
		t.Fatalf("expected import error, got %v", err)
	}
}

func TestBuildGoCreatesUsableDefaultShapesAndImports(t *testing.T) {
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{{ID: "t", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "Events"}, Columns: []compilerir.SemanticColumn{{Name: "when", Scalar: "time", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown}}}}}
	got, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store"})
	if len(diagnostics) != 0 || got.Objects[0].SourceName != "Events" || got.Objects[0].Create == nil || got.Objects[0].Patch == nil || got.Objects[0].Row.DecoderName == "" || len(got.Imports) != 2 {
		t.Fatalf("unexpected Go model: %#v diagnostics=%#v", got, diagnostics)
	}
}
