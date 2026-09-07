package querygen

import (
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestTypedGoSourceUsesCanonicalEvidenceAndImports(t *testing.T) {
	source, err := TypedGoSource(TypedInput{
		Package: "queries", Function: "Find", Engine: "sqlite", SQL: "SELECT amount FROM payments WHERE id = ?",
		Operation: "select", Cardinality: "one", Result: "FindResult", Decoder: "FindDecoder",
		Imports:       []compilerir.GoImport{{Path: "example.com/domain", Alias: "domain"}},
		Parameters:    []TypedValue{{Go: compilerir.GoField{Name: "id", Type: "rasql.Nullable[domain.ID]", Nullable: true}, Semantic: compilerir.SemanticValue{Name: "id", LogicalKind: "uuid", Nullable: true}}},
		ArgumentNames: []string{"id"},
		Results:       []TypedValue{{Go: compilerir.GoField{Name: "amount", Type: "uint64", Codec: "money"}, Semantic: compilerir.SemanticValue{Name: "amount", LogicalKind: "integer", Integer: &compilerir.IntegerTypeFacts{Unsigned: true}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{`"example.com/domain"`, "schema.IntegerType{Unsigned: true", "rasql.NativeProjection", "Value: id"} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated source lacks %q:\n%s", want, text)
		}
	}
}

func TestTypedGoSourceRejectsUnresolvedSchemaType(t *testing.T) {
	_, err := TypedGoSource(TypedInput{Package: "queries", Function: "Find", Engine: "sqlite", SQL: "SELECT value", Operation: "select", Results: []TypedValue{{Go: compilerir.GoField{Name: "value", Type: "string"}, Semantic: compilerir.SemanticValue{Name: "value", LogicalKind: "decimal"}}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported logical type") {
		t.Fatalf("expected unresolved type error, got %v", err)
	}
}
