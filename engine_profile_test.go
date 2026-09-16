package rasql_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

func TestEngineProfile(t *testing.T) {
	t.Run("built-in accessors and opaque custom values", func(t *testing.T) {
		p := rasql.PostgreSQL17()
		require.Equal(t, "postgresql-17", p.ID())
		require.Equal(t, rasql.PostgreSQLEngine, p.Engine())
		require.Equal(t, uint16(17), p.Version().Major)

		require.Equal(t, "postgresql-16", rasql.PostgreSQL16().ID())
		require.Equal(t, "mysql-8.4", rasql.MySQL84().ID())
		require.Equal(t, "sqlite-3.35", rasql.SQLite335().ID())

		custom, err := rasql.NewCustomEngineProfile("adapter", rasql.EngineVersion{Known: true, Major: 1}, rasql.EngineCapabilities{}, rasql.EngineLimits{MaxBindParameters: 10})
		require.NoError(t, err)
		require.Equal(t, rasql.CustomEngine, custom.Engine())
		require.Equal(t, "custom:adapter", custom.ID())
		require.Equal(t, 10, custom.Limits().MaxBindParameters)
	})
}

func TestEngineProfileCompiles(t *testing.T) {
	pass := exec.Command("go", "test", "./testdata/compile/engine_profile")
	if output, err := pass.CombinedOutput(); err != nil {
		t.Fatalf("compile-pass fixture failed: %v\n%s", err, output)
	}
	fail := exec.Command("go", "test", "-tags=compile_fail", "./testdata/compile/engine_profile")
	output, err := fail.CombinedOutput()
	if err == nil {
		t.Fatal("compile-fail fixture unexpectedly passed")
	}
	if !strings.Contains(string(output), "profile") {
		t.Fatalf("compile-fail fixture failed for an unexpected reason: %s", output)
	}
}
