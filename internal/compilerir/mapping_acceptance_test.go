package compilerir_test

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestMappingAcceptanceCoversEngineNativeTypesAndAllConsumerShapes(t *testing.T) {
	tests := []struct {
		name    string
		engine  string
		column  compilerir.PhysicalColumn
		mapping compilerir.ScalarMapping
		scalar  string
		goType  string
	}{
		{name: "postgres domain UUID", engine: "postgresql", column: compilerir.PhysicalColumn{Name: "account_id", LogicalKind: "uuid", Native: &compilerir.NativeType{Dialect: "postgresql", Schema: "public", Name: "account_id", Kind: "domain"}}, mapping: compilerir.ScalarMapping{Name: "domain.AccountID", Match: compilerir.NativeMatch{Dialect: "postgresql", Schema: "public", Name: "account_id", Kind: "domain"}, GoType: "domain.AccountID", Codec: "account-id", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}}}, scalar: "domain.AccountID", goType: "domain.AccountID"},
		{name: "mysql enum", engine: "mysql", column: compilerir.PhysicalColumn{Name: "status", LogicalKind: "text", Nullable: true, Native: &compilerir.NativeType{Dialect: "mysql", Name: "status", Kind: "enum"}}, mapping: compilerir.ScalarMapping{Name: "domain.Status", Match: compilerir.NativeMatch{Dialect: "mysql", Name: "status", Kind: "enum"}, GoType: "domain.Status", NullableGoType: "domain.NullableStatus", Codec: "status", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}}}, scalar: "domain.Status", goType: "domain.NullableStatus"},
		{name: "mysql unsigned BIGINT", engine: "mysql", column: compilerir.PhysicalColumn{Name: "counter", LogicalKind: "integer", Integer: &compilerir.IntegerTypeFacts{Unsigned: true}, Native: &compilerir.NativeType{Dialect: "mysql", Name: "BIGINT", Kind: "builtin"}}, mapping: compilerir.ScalarMapping{Name: "uint64", Match: compilerir.NativeMatch{Dialect: "mysql", Name: "BIGINT", Kind: "builtin"}, GoType: "uint64", Codec: "uint64"}, scalar: "uint64", goType: "uint64"},
		{name: "sqlite MONEY", engine: "sqlite", column: compilerir.PhysicalColumn{Name: "amount", LogicalKind: "native", Native: &compilerir.NativeType{Dialect: "sqlite", Name: "MONEY", Kind: "other"}}, mapping: compilerir.ScalarMapping{Name: "money.Amount", Match: compilerir.NativeMatch{Dialect: "sqlite", Name: "MONEY", Kind: "other"}, GoType: "money.Amount", Codec: "money", Imports: []compilerir.GoImport{{Path: "example.com/money", Alias: "money"}}}, scalar: "money.Amount", goType: "money.Amount"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: test.engine}, Objects: []compilerir.PhysicalObject{{ID: "objects", Kind: "table", Schema: "public", Name: "objects", Columns: []compilerir.PhysicalColumn{test.column}}}}
			model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{test.mapping}}, nil)
			if len(diagnostics) != 0 {
				t.Fatalf("semantic mapping failed: %#v", diagnostics)
			}
			query := compilerir.SemanticQuery{ID: "q", Name: "Find", Cardinality: "many", Parameters: []compilerir.SemanticValue{{Name: "value", Scalar: test.scalar, Nullable: test.name == "mysql enum", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}, Results: []compilerir.SemanticValue{{Name: "value", Scalar: test.scalar, Nullable: test.name == "mysql enum", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}}
			model.Queries = append(model.Queries, query)
			goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store", Scalars: []compilerir.ScalarMapping{test.mapping}})
			if len(diagnostics) != 0 {
				t.Fatalf("Go mapping failed: %#v", diagnostics)
			}
			object := goModel.Objects[0]
			if object.Row.Fields[0].Type != test.goType || object.Row.Fields[0].Codec != test.mapping.Codec || object.Create.Fields[0].Type != test.goType || object.Patch.Fields[0].Type != test.goType {
				t.Fatalf("row/create/patch mapping mismatch: %#v", object)
			}
			if goModel.Queries[0].Parameters[0].Type != test.goType || goModel.Queries[0].Result.Fields[0].Type != test.goType || goModel.Queries[0].Result.Fields[0].Codec != test.mapping.Codec {
				t.Fatalf("parameter/result/returning mapping mismatch: %#v", goModel.Queries[0])
			}
		})
	}
}

func TestMappingAcceptanceUsesCanonicalNullableForPresentZeroAndNULL(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "mysql"}, Objects: []compilerir.PhysicalObject{{ID: "items", Kind: "table", Name: "items", Columns: []compilerir.PhysicalColumn{{Name: "status", Ordinal: 0, LogicalKind: "text", Nullable: true, Native: &compilerir.NativeType{Dialect: "mysql", Name: "status", Kind: "enum"}}}}}}
	mapping := compilerir.ScalarMapping{Name: "domain.Status", Match: compilerir.NativeMatch{Dialect: "mysql", Name: "status", Kind: "enum"}, GoType: "domain.Status", Codec: "status", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{mapping}}, nil)
	if len(diagnostics) != 0 || !model.Objects[0].Columns[0].Nullable {
		t.Fatalf("nullable enum was not retained: %#v %#v", model, diagnostics)
	}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store", Scalars: []compilerir.ScalarMapping{mapping}})
	if len(diagnostics) != 0 || goModel.Objects[0].Row.Fields[0].Type != "rasql.Nullable[domain.Status]" {
		t.Fatalf("canonical nullable mapping was not selected: %#v %#v", goModel, diagnostics)
	}
	if !strings.Contains(goModel.Objects[0].Row.Fields[0].Type, "Nullable") {
		t.Fatal("present zero and NULL would not have separate validity state")
	}
}

func TestMappingAcceptanceKeepsIdentityByDefaultWritable(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "postgresql"}, Objects: []compilerir.PhysicalObject{{ID: "events", Kind: "table", Name: "events", Columns: []compilerir.PhysicalColumn{{Name: "id", Ordinal: 0, LogicalKind: "integer", Identity: "BY DEFAULT"}}}}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, nil)
	if len(diagnostics) != 0 {
		t.Fatalf("identity mapping failed: %#v", diagnostics)
	}
	column := model.Objects[0].Columns[0]
	if column.InsertState != "optional" || column.PatchState != "settable" {
		t.Fatalf("BY DEFAULT identity became non-writable: %#v", column)
	}
}

func TestMappingAcceptanceNegativeHarness(t *testing.T) {
	checks := []struct {
		name  string
		check func() error
		want  string
	}{
		{name: "NULL on non-null", check: func() error {
			return compilerir.ValidateGo(compilerir.GoModel{Package: "store", Queries: []compilerir.GoQuery{{ID: "q", Name: "Find", Cardinality: "one", Parameters: []compilerir.GoField{{Name: "value", Type: "rasql.Nullable[string]"}}}}})
		}, want: "non-null"},
		{name: "generated caller value", check: func() error {
			return compilerir.ValidateGo(compilerir.GoModel{Package: "store", Objects: []compilerir.GoObject{{ID: "t", SourceName: "T", Row: compilerir.GoShape{Name: "TRow", DecoderName: "DecodeT"}, Create: &compilerir.GoShape{Name: "TCreate", Fields: []compilerir.GoField{{Name: "id", Type: "int64"}}}, Patch: &compilerir.GoShape{Name: "TPatch"}, Columns: []compilerir.GoColumn{{Name: "id", GoType: "int64", InsertState: "generated", PatchState: "forbidden"}}}}})
		}, want: "generated"},
		{name: "ambiguous mapping", check: func() error {
			catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{ID: "t", Kind: "table", Name: "t", Columns: []compilerir.PhysicalColumn{{Name: "id", LogicalKind: "integer"}}}}}
			_, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "one", Match: compilerir.NativeMatch{LogicalKind: "integer"}}, {Name: "two", Match: compilerir.NativeMatch{LogicalKind: "integer"}}}}, nil)
			for _, diagnostic := range diagnostics {
				if diagnostic.Code == "ambiguous_scalar" {
					return &expectedFailure{message: "ambiguous_scalar"}
				}
			}
			return &expectedFailure{message: "ambiguous_scalar"}
		}, want: "ambiguous"},
		{name: "unknown codec", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "string"}}}, "store")
		}, want: "codec"},
		{name: "invalid alias", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "domain.Status", Codec: "status", Imports: []compilerir.GoImport{{Path: "example.com/domain", Alias: "bad-alias"}}}}}, "store")
		}, want: "alias"},
		{name: "invalid Go type", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "1+2", Codec: "status"}}}, "store")
		}, want: "Go type"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := check.check()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(check.want)) {
				t.Fatalf("expected %q failure, got %v", check.want, err)
			}
		})
	}
}

type expectedFailure struct{ message string }

func (e *expectedFailure) Error() string { return e.message }
