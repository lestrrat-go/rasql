package compilerconfig_test

import (
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerconfig"
)

func TestDecodeMappingsStrictAndSnakeCase(t *testing.T) {
	config, err := compilerconfig.DecodeMappings([]byte(`{"scalars":[{"name":"domain.Status","match":{"dialect":"mysql","name":"status","kind":"enum"},"go_type":"domain.Status","nullable_go_type":"domain.NullableStatus","imports":[{"path":"example.com/domain","alias":"domain"}],"codec":"status"}]}`), "store")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Scalars) != 1 || config.Scalars[0].Match.Kind != "enum" || config.Scalars[0].Imports[0].Alias != "domain" {
		t.Fatalf("unexpected mapping config: %#v", config)
	}
	if _, err := compilerconfig.DecodeMappings([]byte(`{"scalars":[],"unknown":true}`), "store"); err == nil {
		t.Fatal("unknown mapping field was accepted")
	}
}

func TestDecodeMappingsIncludesOrderedRelations(t *testing.T) {
	data := []byte(`{"scalars":[{"name":"text","match":{"logical_kind":"text"},"go_type":"string","codec":"text"}],"relations":[{"name":"roles","source":"users","from":["tenant_id","id"],"target":"roles","to":["tenant_id","id"],"through":{"object":"user_roles","source_from":["user_tenant","user_id"],"source_to":["tenant_id","id"],"target_from":["role_tenant","role_id"],"target_to":["tenant_id","id"]}}]}`)
	config, err := compilerconfig.DecodeMappings(data, "store")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user_tenant", "user_id"}
	if !reflect.DeepEqual(config.Relations[0].Through.SourceFrom, want) {
		t.Fatalf("unexpected relation paths: %#v", config.Relations[0])
	}
	config.Relations[0].Through.SourceFrom[0] = "changed"
	again, err := compilerconfig.DecodeMappings(data, "store")
	if err != nil {
		t.Fatal(err)
	}
	if again.Relations[0].Through.SourceFrom[0] != want[0] {
		t.Fatalf("decoded relation path aliases source data: %#v", again.Relations[0])
	}
}

func TestDecodeMappingsRejectsUnknownRelationFields(t *testing.T) {
	for name, data := range map[string]string{
		"relation": `{"relations":[{"name":"roles","source":"users","from":["id"],"target":"roles","to":["id"],"through":{"object":"links","source_from":["user_id"],"source_to":["id"],"target_from":["role_id"],"target_to":["id"]},"extra":true}]}`,
		"through":  `{"relations":[{"name":"roles","source":"users","from":["id"],"target":"roles","to":["id"],"through":{"object":"links","source_from":["user_id"],"source_to":["id"],"target_from":["role_id"],"target_to":["id"],"extra":true}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := compilerconfig.DecodeMappings([]byte(data), "store"); err == nil {
				t.Fatal("unknown relation field was accepted")
			}
		})
	}
}
