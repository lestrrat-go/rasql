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

func TestGeneratedInverseRelationshipKeepsConsumerStable(t *testing.T) {
	users := schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id"))
	shipping := schema.ForeignKeyDef{Columns: []string{"shipping_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}
	billing := schema.ForeignKeyDef{Columns: []string{"billing_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}
	memberships := func(keys []schema.ForeignKeyDef, inverse string) schema.TableDef {
		relationship := schema.RelationshipDef{
			Name: "ShippingUser", Kind: schema.RelationshipBelongsTo,
			Columns: []string{"shipping_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"},
			InverseName: inverse,
		}
		return schema.TableDef{Name: "memberships", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}}, {Name: "shipping_user_id", Type: schema.IntegerType{}},
			{Name: "billing_user_id", Type: schema.IntegerType{}},
		}, PrimaryKey: []string{"id"}, ForeignKeys: keys, Relationships: []schema.RelationshipDef{relationship}}
	}
	before := []schema.TableDef{users, memberships([]schema.ForeignKeyDef{shipping}, "")}
	after := []schema.TableDef{users, memberships([]schema.ForeignKeyDef{shipping, billing}, "")}
	overrideBefore := []schema.TableDef{users, memberships([]schema.ForeignKeyDef{shipping}, "Memberships")}
	overrideAfter := []schema.TableDef{users, memberships([]schema.ForeignKeyDef{shipping, billing}, "Memberships")}

	caller := `package store_test

import (
	"context"
	"database/sql"
	"testing"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"example.com/consumer/store"
	_ "modernc.org/sqlite"
)

func TestShippingMemberships(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:"); if err != nil { t.Fatal(err) }; defer database.Close()
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite()); if err != nil { t.Fatal(err) }
	ctx := context.Background()
	if _, err = database.ExecContext(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY); CREATE TABLE memberships (id INTEGER PRIMARY KEY, shipping_user_id INTEGER, billing_user_id INTEGER);"); err != nil { t.Fatal(err) }
	if _, err = database.ExecContext(ctx, "INSERT INTO users VALUES (1), (2); INSERT INTO memberships VALUES (10, 1, 2), (20, 2, 1);"); err != nil { t.Fatal(err) }
	rows, err := store.Users().Memberships().Load(ctx, db, []store.UsersRow{{ID: 1}}); if err != nil { t.Fatal(err) }
	if len(rows[1]) != 1 || rows[1][0].ID != 10 { t.Fatalf("shipping rows = %#v, want membership 10", rows[1]) }
}
`
	beforeDir := t.TempDir()
	writeGeneratedConsumer(t, beforeDir, before, caller)
	runGeneratedConsumer(t, beforeDir, true)
	afterDir := t.TempDir()
	writeGeneratedConsumer(t, afterDir, after, caller)
	runGeneratedConsumer(t, afterDir, false)
	overrideBeforeDir := t.TempDir()
	writeGeneratedConsumer(t, overrideBeforeDir, overrideBefore, caller)
	runGeneratedConsumer(t, overrideBeforeDir, true)
	overrideAfterDir := t.TempDir()
	writeGeneratedConsumer(t, overrideAfterDir, overrideAfter, caller)
	runGeneratedConsumer(t, overrideAfterDir, true)
}

func writeGeneratedConsumer(t *testing.T, dir string, tables []schema.TableDef, caller string) {
	t.Helper()
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	repoGoMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	require.NoError(t, err)
	goMod := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/consumer\n", 1)
	goMod += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600))
	repoGoSum, err := os.ReadFile(filepath.Join("..", "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.sum"), repoGoSum, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "store"), 0o700))
	source, err := generate.PackageSource("store", tables...)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "store", "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer_test.go"), []byte(caller), 0o600))
}

func runGeneratedConsumer(t *testing.T, dir string, wantSuccess bool) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOPROXY=off")
	output, err := command.CombinedOutput()
	if wantSuccess {
		require.NoErrorf(t, err, "consumer output:\n%s", output)
		return
	}
	require.Error(t, err)
	require.Contains(t, string(output), "Memberships")
}

// TestStoreWriteProducesAWorkingPackage is TestGeneratedStorePackageCompilesAndRuns's
// counterpart for the new API: the same scratch-consumer-module setup, the
// same three tables and the same storeAcceptanceTestSource -- both defined
// in acceptance_test.go -- but generated through Store.Write, including one
// Query, instead of through WritePackage plus a hand-assembled query file.
// A single "go test ./..." in the scratch module then compiles every
// generated file as one package and drives a real SQLite round trip through
// it, which is the shape `rasql codegen generate` produces.
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
