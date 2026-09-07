package compilerconfig_test

import (
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
