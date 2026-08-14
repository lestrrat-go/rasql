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
	querytemplate "github.com/lestrrat-go/rasql/template"
	"github.com/stretchr/testify/require"
)

// storeAcceptanceTestSource is the hand-written test placed alongside the
// generated store package inside the scratch consumer module. It exercises
// the generated API at runtime, against a real in-memory SQLite database,
// rather than asserting rasqlgen's output back to itself. See
// examples/store/schema_gen_test.go for the plain-testing style the
// generated descriptor test itself uses, which this file matches by using
// only "testing" and no testify.
const storeAcceptanceTestSource = `package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"example.com/consumer/internal/store"
	_ "modernc.org/sqlite"
)

func TestGeneratedStoreRunsAgainstSQLite(t *testing.T) {
	definition := store.UsersDef()
	if definition.Name != "users" {
		t.Fatalf("UsersDef().Name = %q, want %q", definition.Name, "users")
	}
	if got := len(definition.Columns); got != 2 {
		t.Fatalf("len(UsersDef().Columns) = %d, want 2", got)
	}

	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite database: %s", err)
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection.
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		t.Fatalf("create rasql db: %s", err)
	}
	if err := rasql.CreateTable(ctx, db, store.Users()); err != nil {
		t.Fatalf("create users table: %s", err)
	}
	if _, err := rasql.Insert(ctx, db, store.Users(), store.UsersRow{ID: 1, Email: "ada@example.com"}); err != nil {
		t.Fatalf("insert user: %s", err)
	}

	row, err := rasql.SelectFrom(store.Users()).WhereEqual(store.Users().Email(), "ada@example.com").One(ctx, db)
	if err != nil {
		t.Fatalf("select user: %s", err)
	}
	if row.ID != 1 {
		t.Fatalf("row.ID = %d, want 1", row.ID)
	}

	relationship := store.Orders().User()
	if got := relationship.ParentKey.Name(); got != "id" {
		t.Fatalf("Orders().User().ParentKey.Name() = %q, want %q", got, "id")
	}
	if got := relationship.ChildKey.Name(); got != "user_id" {
		t.Fatalf("Orders().User().ChildKey.Name() = %q, want %q", got, "user_id")
	}

	statement, err := store.UserByEmail("ada@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %s", err)
	}
	if !strings.Contains(statement.SQL(), "SELECT id, email FROM users WHERE email =") {
		t.Fatalf("UserByEmail SQL = %q, want it to contain %q", statement.SQL(), "SELECT id, email FROM users WHERE email =")
	}
	if args := statement.Args(); len(args) != 1 || args[0] != "ada@example.com" {
		t.Fatalf("UserByEmail args = %#v, want [\"ada@example.com\"]", args)
	}
}
`

// TestGeneratedStorePackageCompilesAndRuns is the acceptance test the split
// file layout that "rasqlgen schema" writes has never had: it generates that
// layout, plus a generated query file beside it, into a scratch consumer
// module and runs "go test ./..." there. That single run compiles all four
// generated files as one package and runs both the generated
// schema_gen_test.go and the hand-written test above, which drives a real
// SQLite round trip through the generated descriptor, a generated column
// accessor, a generated relationship method that resolves across files, and
// a generated query function.
//
// Every other test that compiles generated output either builds the
// monolithic layout rasqlgen never writes (internal/schemagen/schema_test.go,
// generate/schema_test.go) or compiles the split layout but only runs the
// generated descriptor test, never a column accessor, a relationship, or a
// query function (cli/rasqlgen/rasqlgen_e2e_test.go). See "1.6 Test
// strategy" for the full accounting.
func TestGeneratedStorePackageCompilesAndRuns(t *testing.T) {
	moduleDir := t.TempDir()

	// Build the scratch module exactly the way
	// cli/rasqlgen/source_test.go's newSchemaSourceFixture does: copy the
	// repository's own go.mod, rather than writing a hand-made "go 1.26"
	// directive, and append a replace directive back to this checkout. That
	// pattern needs no go.sum and no "go mod tidy", and runs offline.
	repoGoMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	// The consumer test below imports modernc.org/sqlite directly, unlike
	// generate.WritePackage's own output, so it needs checksums for that
	// module and its transitive dependencies. The root go.sum already holds
	// them, since the root go.mod requires the same modernc.org/sqlite
	// version; copying it keeps the whole fixture offline under
	// GOPROXY=off, with no "go mod tidy" or network access.
	repoGoSum, err := os.ReadFile(filepath.Join("..", "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))

	outputDir := filepath.Join(moduleDir, "internal", "store")
	require.NoError(t, os.MkdirAll(outputDir, 0o700))

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
	require.NoError(t, generate.WritePackage("store", outputDir, users, orders))

	// Generate a query function file beside the store package, so the test
	// covers the real layout: rasqlgen writes a query function into the same
	// directory as the schema it was compiled against.
	parsed, err := querytemplate.Parse("UserByEmail", `SELECT id, email FROM users WHERE email = {{bind "email"}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.SQLite())
	require.NoError(t, err)
	querySource, err := compiled.GoSource("store", "UserByEmail")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "user_by_email_gen.go"), querySource, 0o600))

	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "acceptance_test.go"), []byte(storeAcceptanceTestSource), 0o600))

	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = moduleDir
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}
