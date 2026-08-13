package rasqlgen_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
}
