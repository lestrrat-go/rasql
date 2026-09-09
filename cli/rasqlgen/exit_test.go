package rasqlgen_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/stretchr/testify/require"
)

func TestExitCodeClassifiesSuccessStaleAndFailure(t *testing.T) {
	require.Equal(t, 0, rasqlgen.ExitCode(nil))
	require.Equal(t, 0, rasqlgen.ExitCode(flag.ErrHelp))
	require.Equal(t, 1, rasqlgen.ExitCode(generate.ErrStale))
	require.Equal(t, 1, rasqlgen.ExitCode(rasqlgen.ErrDrift))
	require.Equal(t, 2, rasqlgen.ExitCode(errors.New("configuration failed")))
}

func writeConfig(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func TestSchemaImportGuardsModeAndDSN(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, writeConfig(filepath.Join(dir, "rasql.json"), `{"engine":{"dialect":"sqlite","profile":"sqlite-3.35"},"schema":{"kind":"live","identity":"live"},"package":"store","output":"store"}`))
	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunTopLevel([]string{"schema", "import", "-config", filepath.Join(dir, "rasql.json")}, &output, &diagnostics)
	require.EqualError(t, err, "schema import: -dsn is required for live sources")
	require.Empty(t, output.String())
	require.Empty(t, diagnostics.String())
}
