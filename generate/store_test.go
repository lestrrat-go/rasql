package generate_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// snapshotDir reads every regular file directly in dir into a name->content
// map, for a before/after comparison that proves a call touched nothing.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	snapshot := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		snapshot[entry.Name()] = string(data)
	}
	return snapshot
}

// TestStorePlanRendersWhatWritePackageWrites is the load-bearing test of
// this PR: for the same two tables, Store.Plan().Files() bytes are
// byte-identical to what generate.WritePackage leaves in a temp directory,
// file for file. This is what makes the later switchover from WritePackage
// to Store a no-op in the generated output.
func TestStorePlanRendersWhatWritePackageWrites(t *testing.T) {
	users, orders := usersTableDef(), ordersTableDef()

	writeDir := t.TempDir()
	require.NoError(t, generate.WritePackage("store", writeDir, users, orders))

	want := make(map[string][]byte, 4)
	for _, name := range []string{"users_gen.go", "orders_gen.go", "schema_gen.go", "schema_gen_test.go"} {
		data, err := os.ReadFile(filepath.Join(writeDir, name))
		require.NoError(t, err)
		want[name] = data
	}

	store := generate.Store{
		Package: "store",
		Dir:     filepath.Join(t.TempDir(), "store"),
		Tables:  []schema.TableDef{users, orders},
	}
	plan, err := store.Plan()
	require.NoError(t, err)

	files := plan.Files()
	require.Len(t, files, 4)
	got := make(map[string][]byte, len(files))
	for _, f := range files {
		got[filepath.Base(f.Path)] = f.Source
	}
	for name, data := range want {
		require.Equal(t, data, got[name], "file %s must be byte-identical to WritePackage's own output", name)
	}
}

// TestStorePlanListsEveryFileInPathOrder pins the exact set of files, and
// their ordering, for two tables and one query: two per-table files, the
// two package-wide files, and the query's own output, sorted by path and
// each an absolute path inside the resolved Dir.
func TestStorePlanListsEveryFileInPathOrder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "user_by_email.sql"), []byte(`SELECT id, email FROM users WHERE email = {{bind "email"}}`), 0o600))

	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef(), ordersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			Input:    "user_by_email.sql",
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)

	files := plan.Files()
	require.Len(t, files, 5)

	wantDir := filepath.Join(root, "store")
	names := make([]string, len(files))
	for i, f := range files {
		require.True(t, filepath.IsAbs(f.Path), "path %s must be absolute", f.Path)
		require.Equal(t, wantDir, filepath.Dir(f.Path))
		names[i] = filepath.Base(f.Path)
		if i > 0 {
			require.Less(t, files[i-1].Path, files[i].Path, "Files() must be sorted by Path")
		}
	}
	require.Equal(t, []string{
		"orders_gen.go",
		"schema_gen.go",
		"schema_gen_test.go",
		"user_by_email_gen.go",
		"users_gen.go",
	}, names)
}

// TestStorePlanWritesNothing confirms Plan is read-only: replanning over an
// already-generated directory leaves every file exactly as it was, and
// planning into a directory that does not exist yet neither creates it nor
// errors because it is missing.
func TestStorePlanWritesNothing(t *testing.T) {
	directory := t.TempDir()
	users := usersTableDef()
	require.NoError(t, generate.WritePackage("store", directory, users))
	before := snapshotDir(t, directory)

	store := generate.Store{Package: "store", Dir: directory, Tables: []schema.TableDef{users}}
	_, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, before, snapshotDir(t, directory))

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	missingStore := generate.Store{Package: "store", Dir: missing, Tables: []schema.TableDef{users}}
	_, err = missingStore.Plan()
	require.NoError(t, err)
	require.NoDirExists(t, missing)
}

// TestStorePlanAppliesHints confirms a Hints entry overlays RowName into
// both the per-table file's row type and the descriptor's literal.
func TestStorePlanAppliesHints(t *testing.T) {
	store := generate.Store{
		Package: "store",
		Dir:     t.TempDir(),
		Tables:  []schema.TableDef{usersTableDef()},
		Hints:   map[string]schema.TableHint{"users": {RowName: "User"}},
	}
	plan, err := store.Plan()
	require.NoError(t, err)

	var usersSource, descriptorSource []byte
	for _, f := range plan.Files() {
		switch filepath.Base(f.Path) {
		case "users_gen.go":
			usersSource = f.Source
		case "schema_gen.go":
			descriptorSource = f.Source
		}
	}
	require.Contains(t, string(usersSource), "type User struct")
	require.Contains(t, string(descriptorSource), `RowName: "User"`)
}

// TestStorePlanRejectsAnUnknownHintKey confirms a hint keyed by a name that
// matches no table in Tables is refused, naming the key.
func TestStorePlanRejectsAnUnknownHintKey(t *testing.T) {
	store := generate.Store{
		Package: "store",
		Dir:     t.TempDir(),
		Tables:  []schema.TableDef{usersTableDef()},
		Hints:   map[string]schema.TableHint{"nope": {RowName: "User"}},
	}
	_, err := store.Plan()
	require.ErrorContains(t, err, `hint key "nope"`)
	require.ErrorContains(t, err, "names no table")
}

// TestStorePlanRejectsAHintKeyMatchingTwoTables confirms a hint key that
// matches two tables -- possible when two SQLite tables of the same name
// live in different schemas -- is refused rather than applied to either,
// naming the key.
func TestStorePlanRejectsAHintKeyMatchingTwoTables(t *testing.T) {
	publicUsers := schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id"))
	auditUsers := schema.MustTableDef("users", schema.Integer("id"), schema.PrimaryKey("id"), schema.InSchema("audit"))
	store := generate.Store{
		Package: "store",
		Dir:     t.TempDir(),
		Tables:  []schema.TableDef{publicUsers, auditUsers},
		Hints:   map[string]schema.TableHint{"users": {RowName: "User"}},
	}
	_, err := store.Plan()
	require.ErrorContains(t, err, `hint key "users"`)
	require.ErrorContains(t, err, "matches more than one table")
}

// TestStorePlanRejectsCollisions covers every way two of a Store's own
// destinations can collide: a case-only table rename, a table literally
// named "schema", a query output equal to another query's, and a query
// output equal to a table's own file.
func TestStorePlanRejectsCollisions(t *testing.T) {
	t.Run("case-only table rename", func(t *testing.T) {
		store := generate.Store{
			Package: "store",
			Dir:     t.TempDir(),
			Tables:  []schema.TableDef{namedTableDef("Users"), namedTableDef("users")},
		}
		_, err := store.Plan()
		require.Error(t, err)
	})

	t.Run("table named schema", func(t *testing.T) {
		table := schema.MustTableDef("schema", schema.Integer("id"), schema.PrimaryKey("id"))
		store := generate.Store{
			Package: "store",
			Dir:     t.TempDir(),
			Tables:  []schema.TableDef{table},
		}
		_, err := store.Plan()
		require.ErrorContains(t, err, "schema_gen.go")
	})

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.sql"), []byte("SELECT 1"), 0o600))

	t.Run("query output collides with another query's", func(t *testing.T) {
		store := generate.Store{
			Package: "store",
			Root:    root,
			Dir:     "store",
			Tables:  []schema.TableDef{usersTableDef()},
			Dialect: dialect.PostgreSQL(),
			Queries: []generate.Query{
				{Input: "a.sql", Function: "QueryOne", Output: "shared_gen.go"},
				{Input: "a.sql", Function: "QueryTwo", Output: "shared_gen.go"},
			},
		}
		_, err := store.Plan()
		require.ErrorContains(t, err, "collides")
	})

	t.Run("query output collides with a table's file", func(t *testing.T) {
		store := generate.Store{
			Package: "store",
			Root:    root,
			Dir:     "store",
			Tables:  []schema.TableDef{usersTableDef()},
			Dialect: dialect.PostgreSQL(),
			Queries: []generate.Query{
				{Input: "a.sql", Function: "QueryOne", Output: "users_gen.go"},
			},
		}
		_, err := store.Plan()
		require.ErrorContains(t, err, "collides")
	})
}

// TestStorePlanRejectsAQueryOutputThatIsNotAPlainFileName covers every way a
// Query.Output that is a path rather than a file name breaks the plan: a ".."
// element that filepath.Join cleans back onto a table's own destination, a
// ".." that escapes Dir, a subdirectory Plan never creates, and an absolute
// path. Each must be an error, and no file may be planned outside Dir.
func TestStorePlanRejectsAQueryOutputThatIsNotAPlainFileName(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte("SELECT 1"), 0o600))

	for _, output := range []string{
		"nested/../users_gen.go",
		"../escape_gen.go",
		"sub/x_gen.go",
		filepath.Join(t.TempDir(), "abs_gen.go"),
	} {
		t.Run(output, func(t *testing.T) {
			store := generate.Store{
				Package: "store",
				Root:    root,
				Dir:     "store",
				Tables:  []schema.TableDef{usersTableDef()},
				Dialect: dialect.PostgreSQL(),
				Queries: []generate.Query{{Input: "q.sql", Function: "Q", Output: output}},
			}
			plan, err := store.Plan()
			require.ErrorContains(t, err, "must be a file name directly inside the store's Dir, not a path")
			require.ErrorContains(t, err, output)
			require.Empty(t, plan.Files(), "a rejected Store must plan no files at all")
		})
	}
}

// TestStorePlanRejectsAQueryFunctionThatRedeclaresAName covers a Query.Function
// that collides with a package-level name the generated files already declare
// -- the table accessor, its table and row types, its definition accessor, and
// the fixed Tables and descriptor-test names -- and with another query's own
// Function. Every one of these compiled to a redeclaration error before the
// check existed.
func TestStorePlanRejectsAQueryFunctionThatRedeclaresAName(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte("SELECT 1"), 0o600))
	newStore := func(queries ...generate.Query) generate.Store {
		return generate.Store{
			Package: "store",
			Root:    root,
			Dir:     "store",
			Tables:  []schema.TableDef{usersTableDef()},
			Dialect: dialect.PostgreSQL(),
			Queries: queries,
		}
	}

	for _, function := range []string{
		"Users",
		"UsersTable",
		"UsersRow",
		"UsersDef",
		"Tables",
		"TestRasqlgenGeneratedDefinitionsAreValid",
	} {
		t.Run(function, func(t *testing.T) {
			plan, err := newStore(generate.Query{Input: "q.sql", Function: function, Output: "q_gen.go"}).Plan()
			require.ErrorContains(t, err, "collides with an identifier the generated store already declares")
			require.ErrorContains(t, err, function)
			require.Empty(t, plan.Files(), "a rejected Store must plan no files at all")
		})
	}

	t.Run("two queries share a function name", func(t *testing.T) {
		plan, err := newStore(
			generate.Query{Input: "q.sql", Function: "Dup", Output: "one_gen.go"},
			generate.Query{Input: "q.sql", Function: "Dup", Output: "two_gen.go"},
		).Plan()
		require.ErrorContains(t, err, `function "Dup" collides with query "Dup"`)
		require.Empty(t, plan.Files(), "a rejected Store must plan no files at all")
	})

	t.Run("a function no generated file declares is accepted", func(t *testing.T) {
		plan, err := newStore(generate.Query{Input: "q.sql", Function: "UserByEmail", Output: "q_gen.go"}).Plan()
		require.NoError(t, err)
		require.Len(t, plan.Files(), 4)
	})
}

// TestStorePlanRejectsInvalidInput covers the rejections Store.Plan makes for
// a missing or malformed field, one field at a time. The two rejections that
// depend on what the rest of the store already claims -- an output that is not
// a plain file name, and a function name something else declares -- have their
// own tests above.
func TestStorePlanRejectsInvalidInput(t *testing.T) {
	validTable := usersTableDef()

	t.Run("empty package", func(t *testing.T) {
		store := generate.Store{Package: "", Dir: t.TempDir(), Tables: []schema.TableDef{validTable}}
		_, err := store.Plan()
		require.Error(t, err)
	})
	t.Run("non-identifier package", func(t *testing.T) {
		store := generate.Store{Package: "not a valid identifier", Dir: t.TempDir(), Tables: []schema.TableDef{validTable}}
		_, err := store.Plan()
		require.Error(t, err)
	})
	t.Run("empty tables", func(t *testing.T) {
		store := generate.Store{Package: "store", Dir: t.TempDir()}
		_, err := store.Plan()
		require.Error(t, err)
	})
	t.Run("empty dir", func(t *testing.T) {
		store := generate.Store{Package: "store", Tables: []schema.TableDef{validTable}}
		_, err := store.Plan()
		require.Error(t, err)
	})

	root := t.TempDir()
	sqlPath := filepath.Join(root, "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))
	newQueryStore := func(q generate.Query) generate.Store {
		return generate.Store{
			Package: "store",
			Root:    root,
			Dir:     "store",
			Tables:  []schema.TableDef{validTable},
			Dialect: dialect.PostgreSQL(),
			Queries: []generate.Query{q},
		}
	}

	t.Run("query missing input", func(t *testing.T) {
		_, err := newQueryStore(generate.Query{Function: "Q", Output: "q_gen.go"}).Plan()
		require.Error(t, err)
	})
	t.Run("query missing function", func(t *testing.T) {
		_, err := newQueryStore(generate.Query{Input: "q.sql", Output: "q_gen.go"}).Plan()
		require.Error(t, err)
	})
	t.Run("query missing output", func(t *testing.T) {
		_, err := newQueryStore(generate.Query{Input: "q.sql", Function: "Q"}).Plan()
		require.Error(t, err)
	})
	t.Run("query output does not end in _gen.go", func(t *testing.T) {
		_, err := newQueryStore(generate.Query{Input: "q.sql", Function: "Q", Output: "q.go"}).Plan()
		require.Error(t, err)
	})
	t.Run("query and store both leave dialect nil", func(t *testing.T) {
		store := generate.Store{
			Package: "store",
			Root:    root,
			Dir:     "store",
			Tables:  []schema.TableDef{validTable},
			Queries: []generate.Query{{Input: "q.sql", Function: "Q", Output: "q_gen.go"}},
		}
		_, err := store.Plan()
		require.Error(t, err)
	})
}

// TestStorePlanResolvesRelativePathsAgainstRoot confirms a relative Dir
// resolves against an explicit Root, and that leaving Root empty with no
// go.mod above the working directory is an error naming Store.Root.
func TestStorePlanResolvesRelativePathsAgainstRoot(t *testing.T) {
	t.Run("relative dir resolves against explicit root", func(t *testing.T) {
		root := t.TempDir()
		store := generate.Store{
			Package: "store",
			Root:    root,
			Dir:     "internal/store",
			Tables:  []schema.TableDef{usersTableDef()},
		}
		plan, err := store.Plan()
		require.NoError(t, err)
		want := filepath.Join(root, "internal", "store")
		for _, f := range plan.Files() {
			require.Equal(t, want, filepath.Dir(f.Path))
		}
	})

	t.Run("empty root with no go.mod above the working directory names Store.Root", func(t *testing.T) {
		outside := t.TempDir()
		require.NoFileExists(t, filepath.Join(outside, "go.mod"))
		t.Chdir(outside)
		store := generate.Store{
			Package: "store",
			Dir:     "internal/store",
			Tables:  []schema.TableDef{usersTableDef()},
		}
		_, err := store.Plan()
		require.ErrorContains(t, err, "Store.Root")
	})
}

// TestStorePlanReportsOrphans exercises every branch of the orphan rule: a
// marker-carrying leftover is reported, a hand-written file is not, a
// marker-carrying file in a subdirectory is not, and a _gen.go file that
// lost its marker is not an orphan but is refused as a destination once the
// plan needs that exact path.
func TestStorePlanReportsOrphans(t *testing.T) {
	directory := t.TempDir()
	users := usersTableDef()
	require.NoError(t, generate.WritePackage("store", directory, users))

	legacyPath := filepath.Join(directory, "legacy_gen.go")
	require.NoError(t, os.WriteFile(legacyPath, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	require.NoError(t, os.WriteFile(filepath.Join(directory, "helpers.go"), []byte("package store\n\nfunc Helper() {}\n"), 0o600))

	subdir := filepath.Join(directory, "sub")
	require.NoError(t, os.MkdirAll(subdir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "nested_gen.go"), []byte(genfile.Marker+"\n\npackage sub\n"), 0o600))

	store := generate.Store{Package: "store", Dir: directory, Tables: []schema.TableDef{users}}
	plan, err := store.Plan()
	require.NoError(t, err)
	require.Equal(t, []string{legacyPath}, plan.Orphans())

	noMarkerPath := filepath.Join(directory, "unrelated_gen.go")
	require.NoError(t, os.WriteFile(noMarkerPath, []byte("package store\n\nfunc NotGenerated() {}\n"), 0o600))
	plan2, err := store.Plan()
	require.NoError(t, err)
	require.NotContains(t, plan2.Orphans(), noMarkerPath)

	sqlPath := filepath.Join(t.TempDir(), "q.sql")
	require.NoError(t, os.WriteFile(sqlPath, []byte("SELECT 1"), 0o600))
	needsThatPath := generate.Store{
		Package: "store",
		Dir:     directory,
		Tables:  []schema.TableDef{users},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: sqlPath, Function: "Unrelated", Output: "unrelated_gen.go"}},
	}
	_, err = needsThatPath.Plan()
	require.ErrorContains(t, err, noMarkerPath)
}

// TestZeroPlanIsNotAPlan confirms the zero Plan reports empty rather than
// panicking; the erroring methods are covered in later PRs.
func TestZeroPlanIsNotAPlan(t *testing.T) {
	var p generate.Plan
	require.Empty(t, p.Files())
	require.Empty(t, p.Orphans())
}

// TestStorePlanDoesNotMutateItsInput confirms the caller's Tables slice and
// its descriptors are untouched by Plan, and that a reordered input plans
// identical bytes.
func TestStorePlanDoesNotMutateItsInput(t *testing.T) {
	users, orders := usersTableDef(), ordersTableDef()
	tables := []schema.TableDef{orders, users}
	before := make([]schema.TableDef, len(tables))
	for i, table := range tables {
		before[i] = table.Clone()
	}

	store := generate.Store{Package: "store", Dir: t.TempDir(), Tables: tables}
	_, err := store.Plan()
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(before, tables), "Plan must not mutate the caller's Tables slice or its descriptors")

	forwardStore := generate.Store{Package: "store", Dir: t.TempDir(), Tables: []schema.TableDef{users, orders}}
	forwardPlan, err := forwardStore.Plan()
	require.NoError(t, err)

	backwardStore := generate.Store{Package: "store", Dir: t.TempDir(), Tables: []schema.TableDef{orders, users}}
	backwardPlan, err := backwardStore.Plan()
	require.NoError(t, err)

	forwardFiles, backwardFiles := forwardPlan.Files(), backwardPlan.Files()
	require.Len(t, backwardFiles, len(forwardFiles))
	for i := range forwardFiles {
		require.Equal(t, filepath.Base(forwardFiles[i].Path), filepath.Base(backwardFiles[i].Path))
		require.Equal(t, forwardFiles[i].Source, backwardFiles[i].Source)
	}
}
