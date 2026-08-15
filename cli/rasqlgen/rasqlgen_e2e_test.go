package rasqlgen_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGoRunInitScaffoldGenerates is the load-bearing test of the init
// command: writing gen/main.go is easy to get superficially right, so this
// proves the scaffold by building and running it, not by comparing it to an
// expected string.
//
// It scaffolds into a scratch consumer module the way
// generate/acceptance_test.go's TestGeneratedStorePackageCompilesAndRuns
// does (copy this repository's go.mod/go.sum, rewrite the module line,
// append a replace back to this checkout, so no "go mod tidy" is needed and
// every step after the "go get" below resolves through the copied go.sum
// and the replace directive), runs "init" through "go run" exactly as a
// real consumer would, generates a store from a real SQLite database
// through the scaffold's own "go generate ./..." directive, and drives "go
// run ./gen -check" through both the clean and the stale state.
//
// This test does not set GOPROXY=off and so does not run offline the way
// generate/acceptance_test.go's fixtures do: it reaches init through the
// same "go get github.com/lestrrat-go/rasql/cmd/rasqlgen@v0.0.0" a real
// consumer runs, and that step is free to consult the module proxy. See
// the comment on env below.
func TestGoRunInitScaffoldGenerates(t *testing.T) {
	moduleDir := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	repoGoMod, err := os.ReadFile(filepath.Join(repository, "go.mod"))
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	// modernc.org/sqlite is already a direct requirement of this
	// repository's own go.mod (it is what cli/rasqlgen itself blank-imports
	// today), so the copied go.mod already requires what the scaffold
	// imports; nothing more needs adding for this test to build.
	repoGoSum, err := os.ReadFile(filepath.Join(repository, "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))

	// GOFLAGS=-buildvcs=false: the scratch module lives under t.TempDir(),
	// and Go's VCS auto-detection has been observed to still try (and fail)
	// to run "git status" when a stray .git directory sits somewhere above
	// the OS temp directory -- see generate/descriptor_roundtrip_test.go's
	// identical guard. Setting it through GOFLAGS, not a per-command flag,
	// is what makes it reach the "go run ." the scaffold's own
	// //go:generate directive spawns, which this test cannot pass flags to
	// directly: that command line is exactly what init writes into the
	// user's file.
	//
	// No GOPROXY=off: unlike generate/acceptance_test.go's fixtures, this
	// test runs `go get github.com/lestrrat-go/rasql/cmd/rasqlgen@v0.0.0`
	// below to reach init the way a real consumer does, and that step is
	// left free to consult the module proxy (a warm module cache may serve
	// it without any network access, but nothing here requires that) -- see
	// cli/rasqlgen/source_test.go's newSchemaSourceFixture doc comment for
	// the same tradeoff on TestGoRunSchemaGeneratesCompilableSource below.
	env := append(os.Environ(), "GOFLAGS=-buildvcs=false")

	command := exec.CommandContext(t.Context(), "go", "get", "github.com/lestrrat-go/rasql/cmd/rasqlgen@v0.0.0")
	command.Dir = moduleDir
	command.Env = env
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	command = exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasqlgen", "init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store")
	command.Dir = moduleDir
	command.Env = env
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "wrote "+filepath.Join("gen", "main.go"))
	require.FileExists(t, filepath.Join(moduleDir, "gen", "main.go"))
	// init runs nothing: no output directory and no database file exist yet.
	_, err = os.Stat(filepath.Join(moduleDir, "internal"))
	require.ErrorIs(t, err, os.ErrNotExist)

	databasePath := filepath.Join(moduleDir, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL)")
	require.NoError(t, err)
	// rasql_schema_migrations is catalog.FromDatabase's own default history
	// table exclusion; it must not show up as a generated file below.
	_, err = database.ExecContext(t.Context(), "CREATE TABLE rasql_schema_migrations (version TEXT PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	generateEnv := append(append([]string{}, env...), "DATABASE_URL="+databasePath)

	command = exec.CommandContext(t.Context(), "go", "generate", "./...")
	command.Dir = moduleDir
	command.Env = generateEnv
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))

	command = exec.CommandContext(t.Context(), "go", "build", "./...")
	command.Dir = moduleDir
	command.Env = env
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))

	storeDir := filepath.Join(moduleDir, "internal", "store")
	require.Equal(t, []string{"posts_gen.go", "schema_gen.go", "schema_gen_test.go", "users_gen.go"}, generatedFileNames(t, storeDir))

	command = exec.CommandContext(t.Context(), "go", "run", "./gen", "-check")
	command.Dir = moduleDir
	command.Env = generateEnv
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))

	database, err = sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "ALTER TABLE users ADD COLUMN nickname TEXT")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	command = exec.CommandContext(t.Context(), "go", "run", "./gen", "-check")
	command.Dir = moduleDir
	command.Env = generateEnv
	output, err = command.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "is stale")

	command = exec.CommandContext(t.Context(), "go", "generate", "./...")
	command.Dir = moduleDir
	command.Env = generateEnv
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))

	command = exec.CommandContext(t.Context(), "go", "run", "./gen", "-check")
	command.Dir = moduleDir
	command.Env = generateEnv
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
}

// TestGoRunSchemaGeneratesCompilableSource runs the command the way a
// consumer does, through go run against a module that replaces rasql, and
// requires the package it writes to build and test. The second half requires
// the same of the package a narrowed rerun leaves behind, which is the case
// the orphan guard exists for.
func TestGoRunSchemaGeneratesCompilableSource(t *testing.T) {
	directory := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	consumer := filepath.Join(directory, "consumer")
	outputDirectory := filepath.Join(consumer, "internal", "store")
	require.NoError(t, os.MkdirAll(outputDirectory, 0o700))
	goMod := "module example.com/consumer\n\ngo 1.26\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(goMod), 0o600))
	databasePath := filepath.Join(consumer, "schema.db")
	usersOutput := filepath.Join(outputDirectory, "users_gen.go")
	ordersOutput := filepath.Join(outputDirectory, "orders_gen.go")

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE orders (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	command := exec.CommandContext(t.Context(), "go", "get", "github.com/lestrrat-go/rasql/cmd/rasqlgen@v0.0.0")
	command.Dir = consumer
	commandOutput, err := command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	command = exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasqlgen", "schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-table", "orders", "-package", "store", "-output", outputDirectory)
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	source, err := os.ReadFile(usersOutput)
	require.NoError(t, err)
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.Contains(t, string(source), `func (t UsersTable) ID() query.ColumnRef { return rasql.ColumnOf(t.Table, "id") }`)
	require.Contains(t, string(source), "func (t UsersTable) As(alias string) (UsersTable, error) {")
	orders, err := os.ReadFile(ordersOutput)
	require.NoError(t, err)
	require.Contains(t, string(orders), "func Orders() OrdersTable {")
	require.NotContains(t, string(orders), "func Users() UsersTable {")
	descriptor, err := os.ReadFile(filepath.Join(outputDirectory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), "var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}")

	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	// Rerunning for one of the two tables would rewrite schema_gen.go
	// without the orders descriptor and leave orders_gen.go reading it, so
	// the run refuses and the package the first run produced still builds.
	command = exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasqlgen", "schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "store", "-output", outputDirectory)
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.Error(t, err, string(commandOutput))
	require.Contains(t, string(commandOutput), "orders_gen.go was generated for table \"orders\"")

	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	// The same rerun succeeds once the file it named is gone, and what it
	// leaves compiles as well.
	require.NoError(t, os.Remove(ordersOutput))
	command = exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasqlgen", "schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "store", "-output", outputDirectory)
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	// A table whose only change is its case generates the same users_gen.go
	// and the same usersTable value, so the run rewrites what is there
	// rather than stranding it, and the guard has to let it through. This is
	// what a migration that recased a table leaves behind, and the package
	// it produces has to build like any other.
	renamedPath := filepath.Join(consumer, "renamed.db")
	database, err = sql.Open("sqlite", renamedPath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE Users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	command = exec.CommandContext(t.Context(), "go", "run", "github.com/lestrrat-go/rasql/cmd/rasqlgen", "schema", "-dsn", renamedPath, "-dialect", "sqlite", "-table", "Users", "-package", "store", "-output", outputDirectory)
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))

	descriptor, err = os.ReadFile(filepath.Join(outputDirectory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), `func UsersDef() schema.TableDef`)
	require.Contains(t, string(descriptor), `"Users"`)
	require.Contains(t, string(descriptor), "var usersTable = UsersTable{")
	require.NoFileExists(t, ordersOutput)

	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))
}

// generatedFileNames reports the sorted names of the regular files directly
// inside dir, failing the test if dir holds a subdirectory.
func generatedFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		require.Falsef(t, entry.IsDir(), "unexpected directory %s in %s", entry.Name(), dir)
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}
