package compilerquery

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

func TestDecodeConfigRejectsTrailingJSONAndDuplicateOutputs(t *testing.T) {
	if _, err := DecodeConfig([]byte(`{"queries":[]} {}`)); err == nil {
		t.Fatal("expected trailing JSON error")
	}
	root := t.TempDir()
	config := Config{ModuleRoot: root, Queries: []QueryConfig{{ID: "a", Input: "a.sql", Engine: "sqlite", Function: "A", Output: "same.go", Operation: "select", Cardinality: "many"}, {ID: "b", Input: "b.sql", Engine: "sqlite", Function: "B", Output: "same.go", Operation: "select", Cardinality: "many"}}}
	if err := ValidateConfig(config, compilerir.EngineIdentity{Dialect: "sqlite"}); err == nil {
		t.Fatal("expected duplicate output error")
	}
}
