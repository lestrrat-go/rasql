package compilerir_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
)

func TestMappingCompilePassFixture(t *testing.T) {
	model := compilerir.SemanticModel{
		Objects: []compilerir.SemanticObject{{ID: "accounts", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "Accounts"}, Columns: []compilerir.SemanticColumn{
			{Name: "id", Scalar: "domain.AccountID", Readable: true, InsertState: "required", PatchState: "settable", Certainty: compilerir.CertaintyKnown},
			{Name: "status", Scalar: "domain.Status", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown},
			{Name: "created", Scalar: "domain.Time", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown},
		}}},
		Queries: []compilerir.SemanticQuery{{ID: "find", Name: "Find", Cardinality: "many", Parameters: []compilerir.SemanticValue{{Name: "status", Scalar: "domain.Status", Nullable: true, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}, Results: []compilerir.SemanticValue{{Name: "id", Scalar: "domain.AccountID", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}, {Name: "status", Scalar: "domain.Status", Nullable: true, TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}}},
	}
	mappings := []compilerir.ScalarMapping{
		{Name: "domain.AccountID", Match: compilerir.NativeMatch{LogicalKind: "uuid"}, GoType: "domain.AccountID", Codec: "account-id", Imports: []compilerir.GoImport{{Path: "mappingfixture/domain", Alias: "domain"}}},
		{Name: "domain.Status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "domain.Status", NullableGoType: "nullable.Status", Codec: "status", Imports: []compilerir.GoImport{{Path: "mappingfixture/domain", Alias: "domain"}, {Path: "mappingfixture/nullable", Alias: "nullable"}}},
		{Name: "domain.Time", Match: compilerir.NativeMatch{LogicalKind: "time"}, GoType: "domain.Time", NullableGoType: "nullable.Time", Codec: "time", Imports: []compilerir.GoImport{{Path: "mappingfixture/domain", Alias: "domain"}, {Path: "mappingfixture/nullable", Alias: "nullable"}}},
	}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "generated", Scalars: mappings})
	if len(diagnostics) != 0 {
		t.Fatalf("mapping output rejected: %#v", diagnostics)
	}
	dir := t.TempDir()
	writeCompileModule(t, dir, renderCompileModel(goModel), []string{"domain", "nullable"})
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	// GOCACHE is deliberately not overridden here: the ambient build cache
	// already holds rasql and its dependencies from the surrounding `go test
	// ./...` run, and a fresh per-call GOCACHE bought no isolation this
	// correctness check needs -- it only forced dependency compilation from
	// scratch on every call.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated mapping fixture failed to compile: %v\n%s", err, output)
	}
}

func TestMappingCompileFailFixture(t *testing.T) {
	dir := t.TempDir()
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{{ID: "accounts", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "Accounts"}, Columns: []compilerir.SemanticColumn{{Name: "status", Scalar: "domain.Status", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown}}}}}
	mapping := compilerir.ScalarMapping{Name: "domain.Status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "domain.Status", NullableGoType: "nullable.Status", Codec: "status", Imports: []compilerir.GoImport{{Path: "mappingfixture/domain", Alias: "domain"}, {Path: "mappingfixture/nullable", Alias: "nullable"}}}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "generated", Scalars: []compilerir.ScalarMapping{mapping}})
	if len(diagnostics) != 0 {
		t.Fatalf("mapping output rejected: %#v", diagnostics)
	}
	source := renderCompileModel(goModel) + "\nvar _ domain.Status = AccountsRow{}.status\n"
	writeCompileModule(t, dir, renderCompileModel(goModel), []string{"domain", "nullable"})
	if err := os.WriteFile(filepath.Join(dir, "consumer.go"), []byte("package generated\n\nimport \"mappingfixture/domain\"\n\n"+source[strings.Index(source, "var _"):]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cannot use") {
		t.Fatalf("compile-fail fixture did not fail for intended type mismatch: %v\n%s", err, output)
	}
}

func TestMappingCompileCanonicalNullableFixture(t *testing.T) {
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{{ID: "events", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "Events"}, Columns: []compilerir.SemanticColumn{{Name: "note", Scalar: "text", Nullable: true, Readable: true, InsertState: "optional", PatchState: "settable", Certainty: compilerir.CertaintyKnown}}}}}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "generated"})
	if len(diagnostics) != 0 || goModel.Objects[0].Row.Fields[0].Type != "rasql.Nullable[string]" {
		t.Fatalf("canonical nullable output failed: %#v %#v", goModel, diagnostics)
	}
	dir := t.TempDir()
	writeCompileModule(t, dir, renderCompileModel(goModel), nil)
	cmd := exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("canonical nullable fixture failed to compile: %v\n%s", err, output)
	}
}

func TestBuildGoOmitsImportsFromUnusedMappings(t *testing.T) {
	model := compilerir.SemanticModel{Objects: []compilerir.SemanticObject{{ID: "events", Kind: "table", PhysicalName: compilerir.QualifiedName{Name: "Events"}, Columns: []compilerir.SemanticColumn{{Name: "id", Scalar: "integer", Readable: true, InsertState: "required", PatchState: "settable", Certainty: compilerir.CertaintyKnown}}}}}
	unused := compilerir.ScalarMapping{Name: "domain.Value", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "domain.Value", Codec: "unused", Imports: []compilerir.GoImport{{Path: "mappingfixture/domain", Alias: "domain"}}}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "generated", Scalars: []compilerir.ScalarMapping{unused}})
	if len(diagnostics) != 0 {
		t.Fatalf("unused mapping changed valid output: %#v", diagnostics)
	}
	for _, imp := range goModel.Imports {
		if imp.Path == "mappingfixture/domain" {
			t.Fatalf("unused mapping import was emitted: %#v", goModel.Imports)
		}
	}
}

func writeCompileModule(t *testing.T, dir, source string, packages []string) {
	t.Helper()
	_, root, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(root), "../.."))
	if err := scratchmod.Write(dir, repo, "mappingfixture"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		fixture := filepath.Join(filepath.Dir(root), "testdata", "mappings", pkg)
		target := filepath.Join(dir, pkg)
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(fixture, pkg+".go"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, pkg+".go"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func renderCompileModel(model compilerir.GoModel) string {
	var out strings.Builder
	out.WriteString("package generated\n\n")
	if len(model.Imports) > 0 {
		out.WriteString("import (\n")
		for _, imp := range model.Imports {
			alias := imp.Alias
			if alias == "" {
				alias = filepath.Base(imp.Path)
			}
			fmt.Fprintf(&out, "\t%s %q\n", alias, imp.Path)
		}
		out.WriteString(")\n\n")
	}
	for _, object := range model.Objects {
		renderShape(&out, object.Row)
		renderShape(&out, *object.Create)
		renderShape(&out, *object.Patch)
	}
	for _, query := range model.Queries {
		if query.Result != nil {
			renderShape(&out, *query.Result)
		}
		out.WriteString("type " + query.Name + "Params struct {\n")
		for _, field := range query.Parameters {
			fmt.Fprintf(&out, "\t%s %s\n", field.Name, field.Type)
		}
		out.WriteString("}\n\n")
	}
	return out.String()
}

func renderShape(out *strings.Builder, shape compilerir.GoShape) {
	fmt.Fprintf(out, "type %s struct {\n", shape.Name)
	for _, field := range shape.Fields {
		fmt.Fprintf(out, "\t%s %s\n", field.Name, field.Type)
	}
	out.WriteString("}\n\n")
}

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

func TestMappingAcceptanceUnsignedFallbackAndQueryAnalysisBoundary(t *testing.T) {
	catalog := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "mysql"}, Objects: []compilerir.PhysicalObject{{ID: "counters", Kind: "table", Name: "counters", Columns: []compilerir.PhysicalColumn{
		{Name: "signed", Ordinal: 0, LogicalKind: "integer", Native: &compilerir.NativeType{Dialect: "mysql", Name: "BIGINT", Kind: "builtin"}},
		{Name: "unsigned", Ordinal: 1, LogicalKind: "integer", Integer: &compilerir.IntegerTypeFacts{Unsigned: true}, Native: &compilerir.NativeType{Dialect: "mysql", Name: "BIGINT", Kind: "builtin"}},
	}}}}
	queries := []compilerir.QueryAnalysis{{ID: "q", Name: "Find", Operation: "select", Cardinality: "many", Parameters: []compilerir.SemanticValue{{Name: "limit", Scalar: "unsigned_integer", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}, Results: []compilerir.SemanticValue{{Name: "unsigned", Scalar: "unsigned_integer", TypeCertainty: compilerir.CertaintyKnown, NullabilityCertainty: compilerir.CertaintyKnown}}}}
	model, diagnostics := compilerir.BuildSemantic(catalog, compilerir.MappingConfig{}, queries)
	if len(diagnostics) != 0 {
		t.Fatalf("unsigned boundary failed: %#v", diagnostics)
	}
	if model.Objects[0].Columns[0].Scalar != "integer" || model.Objects[0].Columns[1].Scalar != "unsigned_integer" {
		t.Fatalf("unexpected physical scalar defaults: %#v", model.Objects[0].Columns)
	}
	goModel, diagnostics := compilerir.BuildGo(model, compilerir.GoConfig{Package: "store"})
	if len(diagnostics) != 0 {
		t.Fatalf("unsigned Go model failed: %#v", diagnostics)
	}
	if goModel.Objects[0].Row.Fields[0].Type != "int64" || goModel.Objects[0].Row.Fields[1].Type != "uint64" || goModel.Objects[0].Create.Fields[1].Type != "uint64" || goModel.Objects[0].Patch.Fields[1].Type != "uint64" {
		t.Fatalf("signed/unsigned object types were not preserved: %#v", goModel.Objects[0])
	}
	if goModel.Queries[0].Parameters[0].Type != "uint64" || goModel.Queries[0].Result.Fields[0].Type != "uint64" {
		t.Fatalf("query analysis unsigned boundary was not preserved: %#v", goModel.Queries[0])
	}
	if catalog.Objects[0].Columns[1].Integer == nil || !catalog.Objects[0].Columns[1].Integer.Unsigned || catalog.Objects[0].Columns[1].Native == nil || catalog.Objects[0].Columns[1].Native.Name != "BIGINT" {
		t.Fatal("physical unsigned/native facts were mutated")
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
			return errors.New("missing diagnostic")
		}, want: "ambiguous"},
		{name: "missing codec reference", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "string"}}}, "store")
		}, want: "codec"},
		{name: "leading digit codec", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "string", Codec: "1codec"}}}, "store")
		}, want: "malformed codec"},
		{name: "invalid character codec", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "string", Codec: "bad/codec"}}}, "store")
		}, want: "malformed codec"},
		{name: "long codec", check: func() error {
			return compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "string", Codec: strings.Repeat("a", 129)}}}, "store")
		}, want: "malformed codec"},
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
	if err := compilerir.ValidateMappingConfig(compilerir.MappingConfig{Scalars: []compilerir.ScalarMapping{{Name: "status", Match: compilerir.NativeMatch{LogicalKind: "text"}, GoType: "string", Codec: "installed-at-runtime"}}}, "store"); err != nil {
		t.Fatalf("valid runtime codec reference was rejected: %v", err)
	}
}

type expectedFailure struct{ message string }

func (e *expectedFailure) Error() string { return e.message }
