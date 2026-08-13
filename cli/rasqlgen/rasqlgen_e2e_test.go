package rasqlgen_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTableOf[UsersRow](schema.MustTableDef(\"users\",")
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.Contains(t, string(source), "\tID query.ColumnRef\n")
	require.Contains(t, string(source), "func (t UsersTable) As(alias string) (UsersTable, error) {")
	orders, err := os.ReadFile(ordersOutput)
	require.NoError(t, err)
	require.Contains(t, string(orders), "func Orders() OrdersTable {")
	require.NotContains(t, string(orders), "func Users() UsersTable {")

	command = exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = consumer
	commandOutput, err = command.CombinedOutput()
	require.NoError(t, err, string(commandOutput))
}
