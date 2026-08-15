package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestStoreWriteProducesAWorkingPackage is TestGeneratedStorePackageCompilesAndRuns's
// counterpart for the new API: the same scratch-consumer-module setup, the
// same three tables and the same storeAcceptanceTestSource -- both defined
// in acceptance_test.go -- but generated through Store.Write, including one
// Query, instead of through WritePackage plus a hand-assembled query file.
// A single "go test ./..." in the scratch module then compiles every
// generated file as one package and drives a real SQLite round trip through
// it, which is the shape a user's own gen/main.go produces.
func TestStoreWriteProducesAWorkingPackage(t *testing.T) {
	moduleDir := t.TempDir()

	// Build the scratch module exactly the way
	// TestGeneratedStorePackageCompilesAndRuns does: copy the repository's
	// own go.mod and go.sum, then add a replace directive back onto this
	// checkout. That needs no "go mod tidy" and runs offline.
	repoGoMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	repoGoSum, err := os.ReadFile(filepath.Join("..", "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))

	// outputDir is left for Store.Write itself to create, unlike
	// TestGeneratedStorePackageCompilesAndRuns, which MkdirAlls it before
	// calling WritePackage -- WritePackage requires the directory to exist
	// already, and Store.Write is the one that does not.
	outputDir := filepath.Join(moduleDir, "internal", "store")

	users := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	orders := schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("user_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("user_id", schema.References("users", "id")),
	)
	// profiles matches TestGeneratedStorePackageCompilesAndRuns's own
	// profiles table exactly: storeAcceptanceTestSource mutates every
	// container this descriptor owns and checks that Tables() hands out an
	// independent copy each time, so every field it reads must be present.
	profiles := schema.TableDef{
		Name: "profiles",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "region", Type: schema.TextType{}},
			{Name: "user_id", Type: schema.IntegerType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{
				Name:              "profiles_email_key",
				Columns:           []string{"email"},
				IncludeColumns:    []string{"id"},
				StorageParameters: map[string]string{"fillfactor": "70"},
				Collations:        map[string]string{"email": "C"},
			},
			{
				Name: "profiles_region_key",
				Keys: []schema.IndexKeyDef{{Expression: "region", Descending: true}},
			},
		},
		Indexes: []schema.IndexDef{
			{
				Name:              "profiles_user_idx",
				Columns:           []string{"user_id"},
				IncludeColumns:    []string{"email"},
				StorageParameters: map[string]string{"fillfactor": "80"},
			},
			{
				Name: "profiles_region_idx",
				Keys: []schema.IndexKeyDef{{Expression: "region", Descending: true}},
			},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name:              "profiles_user_fk",
				Columns:           []string{"user_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
				OnDelete:          schema.SetNull,
				DeleteSetColumns:  []string{"user_id"},
			},
		},
	}

	sqlPath := filepath.Join(moduleDir, "user_by_email.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte(`SELECT id, email FROM users WHERE email = {{bind "email"}}`), 0o600))

	store := generate.Store{
		Package: "store",
		Dir:     outputDir,
		Tables:  []schema.TableDef{users, orders, profiles},
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{
			Input:    sqlPath,
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}
	require.NoError(t, store.Write())

	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "acceptance_test.go"), []byte(storeAcceptanceTestSource), 0o600))

	// Same offline, ambient-cache run as TestGeneratedStorePackageCompilesAndRuns;
	// see that test's own comment for why GOMODCACHE is inherited rather
	// than filled fresh.
	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = moduleDir
	command.Env = append(os.Environ(), "GOPROXY=off")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
	require.NotContainsf(t, string(output), "go: downloading", "go test output:\n%s", output)
}
