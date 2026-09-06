package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTypedQueryCompileFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "wrong comparison", body: `_ = query.EqualValue(store.Users().ID(), "wrong")`, want: "cannot use"},
		{name: "wrong assignment", body: `_ = query.AssignValue(store.Users().ID(), "wrong")`, want: "cannot use"},
		{name: "bare where", body: `_ = rasql.TypedSelectFrom(store.Users()).Where(query.Bind(1))`, want: "cannot use"},
		{name: "mismatched join", body: `_ = query.EqualColumns(store.Users().ID(), store.Users().Email())`, want: "does not match inferred"},
		{name: "null on non-null", body: `_ = query.IsNull(store.Users().ID())`, want: "cannot use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root, err := filepath.Abs("..")
			require.NoError(t, err)
			module := "module example.com/typed-fixture\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(root) + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600))
			source := "package fixture\n\nimport (\n\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/examples/store\"\n\t\"github.com/lestrrat-go/rasql/query\"\n)\n\nvar _ = rasql.Equal\n\nfunc invalid() {\n\t" + tc.body + "\n}\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "invalid.go"), []byte(source), 0o600))
			command := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-run", "^$", "./...")
			command.Dir = dir
			output, err := command.CombinedOutput()
			require.Error(t, err, "invalid typed caller compiled:\n%s", output)
			require.Contains(t, string(output), tc.want)
		})
	}
}
