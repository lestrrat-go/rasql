package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestStorePlanSharesQueryInputSnapshot(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "shared.sql")
	require.NoError(t, os.WriteFile(input, []byte("SELECT id FROM users WHERE id = 1"), 0o600))
	store := Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id"))},
		Dialect: dialect.SQLite(),
		Queries: []Query{
			{Input: "shared.sql", Function: "First", Output: "first_gen.go"},
			{Input: "shared.sql", Function: "Second", Output: "second_gen.go"},
		},
	}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Len(t, plan.inputs, 1)
	files := plan.Files()
	require.Len(t, files, 5)
	sources := make(map[string]string, len(files))
	for _, file := range files {
		sources[filepath.Base(file.Path)] = string(file.Source)
	}
	require.Contains(t, sources["first_gen.go"], "id = 1")
	require.Contains(t, sources["second_gen.go"], "id = 1")
	require.NoError(t, os.WriteFile(input, []byte("SELECT id FROM users WHERE id = 2"), 0o600))
	err = plan.Check()
	require.ErrorContains(t, err, "query input shared.sql changed after Store.Plan")
	require.NotContains(t, err.Error(), "second.sql")
}
