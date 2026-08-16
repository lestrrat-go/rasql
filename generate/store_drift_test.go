package generate_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// storeDriftFixtureDDL builds a database that exercises every fact the
// phase 4 specification's own verification run measured as load-bearing for
// catalog.Drift's two normalization rules (§2.4, §9): orgs/users carries a
// named foreign key, so internal/schemagen derives a RelationshipBelongsTo
// that only the rendered store states; events has no primary key at all, so
// inspection returns a non-nil empty PrimaryKey where the store renders nil.
// users also carries two unnamed CHECKs, an unnamed UNIQUE ... ON CONFLICT,
// and a named UNIQUE, the shapes catalog comparison used to
// under-report (phase 4 specification §1.1).
var storeDriftFixtureDDL = []string{
	`CREATE TABLE orgs (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	)`,
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		org_id INTEGER NOT NULL,
		email TEXT NOT NULL,
		nickname TEXT,
		CHECK (id > 0),
		CHECK (org_id > 0),
		UNIQUE (nickname) ON CONFLICT IGNORE,
		CONSTRAINT users_email_key UNIQUE (email),
		CONSTRAINT users_org_id_fkey FOREIGN KEY (org_id) REFERENCES orgs (id)
	)`,
	`CREATE TABLE events (
		id INTEGER,
		payload TEXT
	)`,
}

// TestDriftAgainstAGeneratedStoreIsEmpty is the false-positive guard §2.4
// promises and §7 (PR 1) requires: a store generated from a real inspection
// must report no drift against a fresh inspection of the same, unchanged
// database. store.Tables() only exists once the generated package is
// compiled, so the comparison itself has to run inside a scratch consumer
// module, built exactly the way TestGeneratedStorePackageCompilesAndRuns
// builds one -- copy this repository's own go.mod and go.sum, rewrite the
// module line, and append a replace back to this checkout, which needs no
// "go mod tidy" and runs offline.
//
// This does not weaken TestGenerateDoesNotImportCatalog
// (catalog/catalog_test.go:330-336): that test runs "go list -deps
// github.com/lestrrat-go/rasql/generate" against this repository's own
// module, which this test does not touch. The scratch module's own test
// file below is a downstream consumer importing both catalog and the
// generated store, which is the arrangement being tested, not a new import
// inside generate itself.
func TestDriftAgainstAGeneratedStoreIsEmpty(t *testing.T) {
	moduleDir := t.TempDir()

	// The fixture database lives inside the scratch module's own directory,
	// as a plain file, so the consumer module's test process -- a separate
	// "go test" run below -- can open the very same database fresh, rather
	// than sharing a *sql.DB across two processes.
	dbPath := filepath.Join(moduleDir, "fixture.db")
	database, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	for _, statement := range storeDriftFixtureDDL {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())

	freshRead, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = freshRead.Close() })
	tables, err := catalog.FromDatabase(context.Background(), freshRead, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Len(t, tables, 3)
	require.NoError(t, freshRead.Close())

	// Build the scratch module exactly the way
	// TestGeneratedStorePackageCompilesAndRuns does.
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

	outputDir := filepath.Join(moduleDir, "internal", "store")
	require.NoError(t, os.MkdirAll(outputDir, 0o700))
	require.NoError(t, generate.WritePackage("store", outputDir, tables...))

	testSource := fmt.Sprintf(storeDriftConsumerTestSource, filepath.ToSlash(dbPath))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "drift_test.go"), []byte(testSource), 0o600))

	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = moduleDir
	command.Env = append(os.Environ(), "GOPROXY=off")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
	require.NotContainsf(t, string(output), "go: downloading", "go test output:\n%s", output)
}

// storeDriftConsumerTestSource is written into the scratch module's own
// store package. It is the one place catalog.Drift(store.Tables(), live) is
// actually called: store.Tables() only exists once the generated package
// compiles, and this file runs inside the module that just compiled it. It
// uses only "testing", matching examples/store/schema_gen_test.go's plain
// style, since a generated package's own test is not the place to add a new
// test-only dependency.
const storeDriftConsumerTestSource = `package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"example.com/consumer/internal/store"
	_ "modernc.org/sqlite"
)

func TestGeneratedStoreReportsNoDriftAgainstItsOwnDatabase(t *testing.T) {
	database, err := sql.Open("sqlite", %q)
	if err != nil {
		t.Fatalf("open sqlite database: %%s", err)
	}
	defer func() { _ = database.Close() }()

	live, err := catalog.FromDatabase(context.Background(), database, catalog.Options{Dialect: dialect.SQLite()})
	if err != nil {
		t.Fatalf("catalog.FromDatabase: %%s", err)
	}

	report := catalog.Drift(store.Tables(), live)
	if !report.Empty() {
		t.Fatalf("catalog.Drift(store.Tables(), live) is not empty:\nadded: %%+v\nremoved: %%+v\nchanged: %%+v", report.Added(), report.Removed(), report.Changed())
	}
}
`
