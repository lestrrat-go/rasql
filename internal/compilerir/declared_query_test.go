package compilerir

import "testing"

func TestDeclaredQueryLogicalKindUsesOnlyExplicitLogicalMappings(t *testing.T) {
	for _, scalar := range []string{"boolean", "integer", "float", "text", "bytes", "time", "json", "uuid", "decimal"} {
		got, ok, err := DeclaredQueryLogicalKind(scalar, MappingConfig{})
		if err != nil || !ok || got != scalar {
			t.Fatalf("scalar %q: got %q, %v, %v", scalar, got, ok, err)
		}
	}
	got, ok, err := DeclaredQueryLogicalKind("unsigned_integer", MappingConfig{})
	if err != nil || !ok || got != "integer" {
		t.Fatalf("unsigned: got %q, %v, %v", got, ok, err)
	}
	got, ok, err = DeclaredQueryLogicalKind("custom", MappingConfig{Scalars: []ScalarMapping{{Name: "custom", GoType: "int", Codec: "codec", Match: NativeMatch{Dialect: "postgresql", Name: "int4", LogicalKind: "integer"}}}})
	if err != nil || !ok || got != "integer" {
		t.Fatalf("custom: got %q, %v, %v", got, ok, err)
	}
	_, ok, err = DeclaredQueryLogicalKind("native", MappingConfig{Scalars: []ScalarMapping{{Name: "native", GoType: "int", Codec: "codec", Match: NativeMatch{Dialect: "postgresql", Name: "int4"}}}})
	if err != nil || ok {
		t.Fatalf("native-only mapping matched: %v, %v", ok, err)
	}
}
