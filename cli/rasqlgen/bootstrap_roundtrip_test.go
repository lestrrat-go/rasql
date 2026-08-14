package rasqlgen_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// normalizeEmptySlices clears the one cosmetic asymmetry a table with no
// checks and no exclusion constraints picks up crossing the literal writer:
// writeTableDefLiteral omits a zero-length slice field from the literal it
// writes, so a table whose Checks or ExclusionConstraints started as a
// non-nil empty slice comes back nil once its description has been compiled
// and read back, while the other of the two may go the opposite way
// depending on how inspection populated it. Neither direction is a real
// difference in what the table describes, so both sides of the comparison
// are normalized the same way before comparing.
func normalizeEmptySlices(table *schema.TableDef) {
	if table.Checks == nil {
		table.Checks = []schema.CheckDef{}
	}
	if table.ExclusionConstraints == nil {
		table.ExclusionConstraints = []schema.ExclusionDef{}
	}
}

// newBootstrapRoundTripModule builds a scratch Go module the same way
// newSchemaSourceFixture in source_test.go does -- a replace directive back
// to this checkout, no go.sum, so the fixture runs under the ambient module
// proxy settings without reaching the network -- but leaves internal/tables
// empty for bootstrap itself to fill, rather than pre-writing a schema
// package by hand.
func newBootstrapRoundTripModule(t *testing.T) string {
	t.Helper()
	moduleDir := t.TempDir()

	repoGoMod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "internal", "tables"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "internal", "store"), 0o700))
	return moduleDir
}

// verifyProgramSource is the throwaway program
// TestBootstrapRoundTripsThroughSchemaSource runs to read the generated
// store package's descriptors back out: it cannot be imported into the test
// binary itself, because the package it imports does not exist until the
// test has already generated it.
const verifyProgramSource = `package main

import (
	"fmt"

	"example.com/consumer/internal/store"
	"github.com/lestrrat-go/rasql/schema"
)

func main() {
	tables := map[string]schema.TableDef{
		"users":  store.UsersDef(),
		"orders": store.OrdersDef(),
	}
	for _, name := range []string{"users", "orders"} {
		table := tables[name]
		// Relationships is an annotation rasqlgen schema derives from
		// ForeignKeys itself, never something inspection reports or the
		// description package states, so it is excluded from this
		// round-trip comparison.
		table.Relationships = nil
		if table.Checks == nil {
			table.Checks = []schema.CheckDef{}
		}
		if table.ExclusionConstraints == nil {
			table.ExclusionConstraints = []schema.ExclusionDef{}
		}
		fmt.Printf("%s\t%#v\n", name, table)
	}
}
`

// TestBootstrapRoundTripsThroughSchemaSource is the acceptance test for
// bootstrap: it inspects a live SQLite schema, writes the description
// package bootstrap generates, runs `rasqlgen schema -source` over that
// package, and requires the resulting store descriptors to equal what was
// inspected directly. That equality is the only check proving bootstrap's
// output is genuinely what -source consumes -- the two commands have to
// chain without a human editing anything in between. The fixture also
// includes the default migration history table, so a successful round trip
// also proves the sweep's default skip survived the whole chain.
//
// This test spawns two real `go run` invocations: schema -source's own
// internal one, and the verify program above.
func TestBootstrapRoundTripsThroughSchemaSource(t *testing.T) {
	moduleDir := newBootstrapRoundTripModule(t)
	databasePath := filepath.Join(moduleDir, "schema.db")

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	for _, statement := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, created_at TIMESTAMP NOT NULL)",
		"CREATE UNIQUE INDEX users_email_idx ON users (email)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id), total_cents INTEGER NOT NULL)",
		"CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY, checksum TEXT NOT NULL)",
	} {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}

	ctx := t.Context()
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	inspector, err := inspect.New(tx, dialect.SQLite())
	require.NoError(t, err)
	expected := make(map[string]string, 2)
	for _, name := range []string{"users", "orders"} {
		table, err := inspector.TableIn(ctx, "main", name)
		require.NoError(t, err)
		normalizeEmptySlices(&table)
		expected[name] = fmt.Sprintf("%s\t%#v", name, table)
	}
	require.NoError(t, tx.Rollback())
	require.NoError(t, database.Close())

	t.Chdir(moduleDir)

	var buffer bytes.Buffer
	require.NoError(t, rasqlgen.Run([]string{"bootstrap", "-dsn", databasePath, "-dialect", "sqlite", "-package", "schemasource", "-output", "internal/tables"}, &buffer), buffer.String())
	require.FileExists(t, filepath.Join(moduleDir, "internal", "tables", "users_gen.go"))
	require.FileExists(t, filepath.Join(moduleDir, "internal", "tables", "orders_gen.go"))
	require.FileExists(t, filepath.Join(moduleDir, "internal", "tables", "tables_gen.go"))
	require.NoFileExists(t, filepath.Join(moduleDir, "internal", "tables", "rasql_schema_migrations_gen.go"))

	buffer.Reset()
	require.NoError(t, rasqlgen.Run([]string{"schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store"}, &buffer), buffer.String())

	verifyDir := filepath.Join(moduleDir, "cmd", "verify")
	require.NoError(t, os.MkdirAll(verifyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(verifyDir, "main.go"), []byte(verifyProgramSource), 0o600))

	command := exec.CommandContext(t.Context(), "go", "run", "./cmd/verify")
	command.Dir = moduleDir
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
	for _, name := range []string{"users", "orders"} {
		found := false
		for _, line := range lines {
			if !strings.HasPrefix(line, name+"\t") {
				continue
			}
			require.Equal(t, expected[name], line, "descriptor for %q must equal what was inspected", name)
			found = true
		}
		require.True(t, found, "verify program did not report %q", name)
	}
}
