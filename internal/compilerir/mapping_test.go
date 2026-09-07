package compilerir_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestMappingDefaultsAndQualifiedPrecedence(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "postgresql"}, Objects: []compilerir.PhysicalObject{{
		ID: "accounts", Kind: "table", Schema: "public", Name: "accounts",
		Columns: []compilerir.PhysicalColumn{
			{Name: "id", Ordinal: 0, LogicalKind: "uuid", Native: &compilerir.NativeType{Dialect: "postgresql", Schema: "public", Name: "account_id", Kind: "domain"}},
			{Name: "count", Ordinal: 1, LogicalKind: "integer"},
		},
	}}}
	config := compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{
		{Name: "domain.AccountID", Match: compilerir.NativeMatch{Dialect: "postgresql", Schema: "public", Name: "account_id", Kind: "domain"}, GoType: "domain.AccountID", Codec: "account-id"},
		{Name: "uuid-wide", Match: compilerir.NativeMatch{Dialect: "postgresql", Name: "account_id"}, GoType: "string", Codec: "uuid"},
	}}
	model, diagnostics := compilerir.BuildSemantic(catalog, config, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	if got := model.Objects[0].Columns[0].Scalar; got != "domain.AccountID" {
		t.Fatalf("qualified mapping lost precedence: %q", got)
	}
	if got := model.Objects[0].Columns[1].Scalar; got != "integer" {
		t.Fatalf("integer default changed: %q", got)
	}
}

func TestBuildGoUsesCustomNullableAndCodecAcrossShapes(t *testing.T) {
	model := compilerir.SemanticModel{
		Objects: []compilerir.SemanticObject{{ID: "accounts", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "accounts"}, Columns: []compilerir.SemanticColumn{{Name: "status", Scalar: "domain.Status", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown}}}},
		Queries: []compilerir.SemanticQuery{{ID: "q", Name: "Find", Cardinality: "many", Parameters: []compilerir.SemanticValue{{Name: "status", Scalar: "domain.Status", Nullable: true, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}, Results: []compilerir.SemanticValue{{Name: "status", Scalar: "domain.Status", Nullable: true, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}}},
	}
	config := compilerir.GoConfig{Package: "store", Scalars: []compilerir.ScalarMapping{{Name: "domain.Status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "domain.Status", NullableGoType: "domain.NullableStatus", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}}, Codec: "status"}}}
	got, diagnostics := compilerir.BuildGo(model, config)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	if got.Objects[0].Columns[0].GoType != "domain.NullableStatus" || got.Objects[0].Columns[0].Codec != "status" {
		t.Fatalf("object mapping not applied: %#v", got.Objects[0].Columns[0])
	}
	if got.Objects[0].Row.Fields[0].Type != "domain.NullableStatus" || got.Objects[0].Row.Fields[0].Codec != "status" || got.Objects[0].Create.Fields[0].Codec != "status" || got.Objects[0].Patch.Fields[0].Codec != "status" {
		t.Fatalf("mapping did not reach all object shapes: %#v", got.Objects[0])
	}
	if got.Queries[0].Parameters[0].Type != "domain.NullableStatus" || got.Queries[0].Parameters[0].Codec != "status" || got.Queries[0].Result.Fields[0].Codec != "status" {
		t.Fatalf("mapping did not reach query shapes: %#v", got.Queries[0])
	}
}

func TestValidateMappingConfigRejectsInvalidGoAndDuplicateNames(t *testing.T) {
	config := compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", GoType: "domain.Status", Codec: "status", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "bad-alias"}}}, {Name: "status", GoType: "string", Codec: "status"}}}
	if err := compilerir.ValidateMappingConfig(config, "store"); err == nil {
		t.Fatal("invalid alias and duplicate mapping were accepted")
	}
}

func TestBuildSemanticRejectsUnmatchedOpaqueMapping(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "payments", Kind: "table", Name: "payments", Columns: []compilerir.PhysicalColumn{{Name: "amount", Ordinal: 0, LogicalKind: "native", Native: &compilerir.NativeType{Dialect: "sqlite", Name: "MONEY", Kind: "other"}}}}}}
	_, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) == 0 || diagnostics[0].Code != "opaque_type" || diagnostics[0].Path != "payments.amount" {
		t.Fatalf("unexpected opaque diagnostic: %#v", diagnostics)
	}
}
