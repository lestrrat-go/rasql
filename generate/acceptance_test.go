package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
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
	"fmt"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
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

	// Tables reports every table this store describes, in the declaration
	// order schema_gen.go states them: alphabetically, so "orders" before
	// "profiles" before "users".
	tables := store.Tables()
	if len(tables) != 3 {
		t.Fatalf("len(Tables()) = %d, want 3", len(tables))
	}
	if tables[0].Name != "orders" || tables[1].Name != "profiles" || tables[2].Name != "users" {
		t.Fatalf("Tables() order = [%q, %q, %q], want [\"orders\", \"profiles\", \"users\"]", tables[0].Name, tables[1].Name, tables[2].Name)
	}
	// Mutating what Tables returned must not reach the package-level value
	// the next call reads from, the same guarantee UsersDef already gives a
	// caller of a single table's descriptor. The test below takes that to
	// every container a descriptor owns; this is the shallow case.
	tables[0].Name = "mutated"
	tables[0].Columns[0].Name = "mutated"
	again := store.Tables()
	if again[0].Name != "orders" {
		t.Fatalf("Tables() second call Name = %q, want %q; mutating the first result must not affect the next call", again[0].Name, "orders")
	}
	if again[0].Columns[0].Name == "mutated" {
		t.Fatalf("Tables() second call Columns[0].Name was mutated by an earlier caller; Clone must produce an independent copy")
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

// TestGeneratedTablesHandOutIndependentDescriptors drives the generated
// Tables() function itself, rather than rasqlgen's output read back as
// text: it mutates every container of a descriptor Tables() returned and
// requires that nothing a later caller reads changed. The "profiles" table
// exists for this, carrying a nested container on every descriptor field
// that owns one, so a container handed out by reference instead of by copy
// fails here rather than reaching a caller of a generated store.
func TestGeneratedTablesHandOutIndependentDescriptors(t *testing.T) {
	tables := store.Tables()
	if len(tables) != 3 || tables[1].Name != "profiles" {
		t.Fatalf("Tables()[1].Name = %q over %d tables, want %q over 3", tables[1].Name, len(tables), "profiles")
	}

	generated := describeProfiles(tables[1])
	mutateEveryContainer(tables[1])
	if mutated := describeProfiles(tables[1]); mutated == generated {
		t.Fatalf("mutateEveryContainer changed nothing describeProfiles reports:\n%s\nthe two must describe the same containers", mutated)
	}
	if got := describeProfiles(store.Tables()[1]); got != generated {
		t.Fatalf("Tables() after an earlier result was mutated =\n%s\nwant\n%s", got, generated)
	}
	if got := describeProfiles(store.ProfilesDef()); got != generated {
		t.Fatalf("ProfilesDef() after a Tables() result was mutated =\n%s\nwant\n%s\nTables must not share state with the package-level descriptor", got, generated)
	}

	// Two results held at once must not share state with each other either.
	held := store.Tables()[1]
	mutateEveryContainer(store.Tables()[1])
	if got := describeProfiles(held); got != generated {
		t.Fatalf("a descriptor from one Tables() call was changed through another call's =\n%s\nwant\n%s", got, generated)
	}
}

// describeProfiles reports every container of the "profiles" descriptor
// that schema.TableDef.Clone has to copy, so a container the generated
// Tables() hands out by reference instead of by copy shows up as a changed
// description. fmt prints a map in sorted key order, so the result is
// stable across runs, and an added key changes it.
func describeProfiles(table schema.TableDef) string {
	columns := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		columns = append(columns, column.Name)
	}
	unique := table.UniqueConstraints[0]
	keyedUnique := table.UniqueConstraints[1]
	index := table.Indexes[0]
	keyedIndex := table.Indexes[1]
	foreignKey := table.ForeignKeys[0]
	return fmt.Sprintf(
		"columns=%v primaryKey=%v\nunique=%v include=%v storage=%v collations=%v keys=%v\nindex=%v include=%v storage=%v keys=%v\nforeignKey=%v referenced=%v deleteSet=%v\nrelationship=%v",
		columns, table.PrimaryKey,
		unique.Columns, unique.IncludeColumns, unique.StorageParameters, unique.Collations, keyedUnique.Keys,
		index.Columns, index.IncludeColumns, index.StorageParameters, keyedIndex.Keys,
		foreignKey.Columns, foreignKey.ReferencedColumns, foreignKey.DeleteSetColumns,
		table.Relationships[0].Columns,
	)
}

// mutateEveryContainer writes through every container describeProfiles
// reports, and adds a key to each map, which is the mutation a caller of
// Tables is free to make on what it was handed.
func mutateEveryContainer(table schema.TableDef) {
	table.Columns[0].Name = "mutated"
	table.PrimaryKey[0] = "mutated"
	table.Relationships[0].Columns[0] = "mutated"

	unique := table.UniqueConstraints[0]
	unique.Columns[0] = "mutated"
	unique.IncludeColumns[0] = "mutated"
	unique.StorageParameters["fillfactor"] = "999"
	unique.StorageParameters["mutated"] = "mutated"
	unique.Collations["email"] = "mutated"
	unique.Collations["mutated"] = "mutated"
	table.UniqueConstraints[1].Keys[0].Expression = "mutated"

	index := table.Indexes[0]
	index.Columns[0] = "mutated"
	index.IncludeColumns[0] = "mutated"
	index.StorageParameters["fillfactor"] = "999"
	index.StorageParameters["mutated"] = "mutated"
	table.Indexes[1].Keys[0].Expression = "mutated"

	foreignKey := table.ForeignKeys[0]
	foreignKey.Columns[0] = "mutated"
	foreignKey.ReferencedColumns[0] = "mutated"
	foreignKey.DeleteSetColumns[0] = "mutated"
}
`

// TestGeneratedStorePackageCompilesAndRuns is the acceptance test for the
// split file layout generate.Store writes: it generates that layout, plus a
// generated query file beside it, into a scratch consumer
// module and runs "go test ./..." there. That single run compiles every
// generated file as one package and runs both the generated
// schema_gen_test.go and the hand-written tests above, which drive a real
// SQLite round trip through the generated descriptor, a generated column
// accessor, a generated relationship method that resolves across files, a
// generated query function, and the package-level Tables function that
// aggregates every table's descriptor, including whether what Tables hands
// back shares any container with what the next call returns.
//
// The other source-generation tests cover individual generated files or
// specific package-level helpers.
func TestGeneratedStorePackageCompilesAndRuns(t *testing.T) {
	moduleDir := t.TempDir()

	// Build the scratch module exactly the way
	// Copy the repository's own go.mod, rather than writing a hand-made "go 1.26"
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
	// version. Copying it is what lets the offline environment built below
	// verify every module the scratch build resolves, with no "go mod tidy"
	// and no network access.
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
	// profiles is the descriptor the consumer test mutates: it carries a
	// nested container on every schema.TableDef field that owns one, so the
	// generated Tables() has something to share if its clone is ever less
	// than deep. Several of these facts are describable but not renderable,
	// which is why nothing creates this table in SQLite; they exist here to
	// be described, cloned and mutated.
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
	require.NoError(t, generate.WritePackage("store", outputDir, users, orders, profiles))

	// Generate a query function file beside the store package, so the test
	// covers the real layout: rasqlgen writes a query function into the same
	// directory as the schema it was compiled against.
	parsed, err := namedsql.Parse("UserByEmail", `SELECT id, email FROM users WHERE email = {{bind "email"}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.SQLite())
	require.NoError(t, err)
	querySource, err := querygen.GoSource(compiled.QueryDef(), "store", "UserByEmail")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "user_by_email_gen.go"), querySource, 0o600))

	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "acceptance_test.go"), []byte(storeAcceptanceTestSource), 0o600))

	// The scratch module's toolchain run gets an explicit environment rather
	// than the ambient one, so an offline build is what this test enforces
	// instead of what it happens to inherit: GOPROXY=off makes a fetch fail
	// instead of silently succeeding against whatever proxy the developer or
	// CI has configured. Everything else is inherited, because the toolchain
	// still needs PATH, HOME and the rest of its ambient environment.
	//
	// The module cache is inherited too, deliberately. A scratch GOMODCACHE
	// would have to be filled before the offline run, and the step that
	// fills it, "go mod download", resolves every module the copied go.mod
	// requires rather than the ones the fixture imports. That reaches for
	// indirect and test-only modules no package here imports, which a
	// machine that only ever built and tested this repository never had to
	// download, so filling the scratch cache fails there even though the
	// fixture itself builds. Reading the inherited cache resolves only what
	// the fixture imports, and gives up no guarantee: GOPROXY=off is what
	// makes an unavailable module a failure, and a warm build cache cannot
	// stand in for one, because the toolchain has to read a module's source
	// to key the cache at all.
	command := exec.CommandContext(t.Context(), "go", "test", "./...")
	command.Dir = moduleDir
	command.Env = append(os.Environ(), "GOPROXY=off")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
	// A run that reached for a module prints "go: downloading" before
	// GOPROXY=off refuses it, so the absence of that line is what shows the
	// modules already on this machine alone satisfied the build.
	require.NotContainsf(t, string(output), "go: downloading", "go test output:\n%s", output)
}
