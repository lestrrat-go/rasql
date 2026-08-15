package rasqlgen

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitWritesTheScaffold requires that init writes exactly one file,
// gen/main.go, and that the file it writes parses as Go source and is
// already gofmt-clean: format.Source must return the same bytes it was
// given.
func TestInitWritesTheScaffold(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}, &out))

	path := filepath.Join("gen", "main.go")
	source, err := os.ReadFile(path)
	require.NoError(t, err)

	fset := token.NewFileSet()
	_, err = parser.ParseFile(fset, path, source, parser.ParseComments)
	require.NoError(t, err)

	formatted, err := format.Source(source)
	require.NoError(t, err)
	require.Equal(t, string(formatted), string(source))

	entries, err := os.ReadDir("gen")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "main.go", entries[0].Name())
}

// TestInitWritesTheRequestedDialect requires that each accepted -dialect
// spelling writes the matching driver import, sql.Open driver name, and
// dialect.*() call -- each exactly twice, once in catalog.Options and once
// in Store.Dialect.
func TestInitWritesTheRequestedDialect(t *testing.T) {
	testCases := []struct {
		dialect      string
		driverImport string
		openName     string
		dialectCall  string
	}{
		{dialect: "postgresql", driverImport: "github.com/jackc/pgx/v5/stdlib", openName: "pgx", dialectCall: "dialect.PostgreSQL()"},
		{dialect: "postgres", driverImport: "github.com/jackc/pgx/v5/stdlib", openName: "pgx", dialectCall: "dialect.PostgreSQL()"},
		{dialect: "mysql", driverImport: "github.com/go-sql-driver/mysql", openName: "mysql", dialectCall: "dialect.MySQL()"},
		{dialect: "sqlite", driverImport: "modernc.org/sqlite", openName: "sqlite", dialectCall: "dialect.SQLite()"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.dialect, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out bytes.Buffer
			require.NoError(t, Run([]string{"init", "-dialect", testCase.dialect, "-package", "store", "-output", "internal/store"}, &out))

			source, err := os.ReadFile(filepath.Join("gen", "main.go"))
			require.NoError(t, err)
			text := string(source)

			require.Contains(t, text, `_ "`+testCase.driverImport+`"`)
			require.Contains(t, text, `sql.Open("`+testCase.openName+`", dsn)`)
			require.Equal(t, 2, bytes.Count(source, []byte(testCase.dialectCall)),
				"%s must appear twice (catalog.Options and Store.Dialect), got:\n%s", testCase.dialectCall, text)
		})
	}
}

// TestInitWritesPackageAndOutput requires that -package and -output land in
// Store.Package and Store.Dir.
func TestInitWritesPackageAndOutput(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "acct", "-output", "pkg/acct"}, &out))

	source, err := os.ReadFile(filepath.Join("gen", "main.go"))
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, `Package: "acct"`)
	require.Contains(t, text, `Dir:     "pkg/acct"`)
}

// TestInitRefusesAnExistingFile requires that init leaves an existing
// gen/main.go untouched, and names the path plus -force in its error, when
// -force is not set.
func TestInitRefusesAnExistingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("gen", 0o755))
	original := []byte("package main\n\n// hand-edited\n")
	require.NoError(t, os.WriteFile(filepath.Join("gen", "main.go"), original, 0o644))

	var out bytes.Buffer
	err := Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}, &out)
	require.Error(t, err)
	require.ErrorContains(t, err, filepath.Join("gen", "main.go"))
	require.ErrorContains(t, err, "-force")

	after, readErr := os.ReadFile(filepath.Join("gen", "main.go"))
	require.NoError(t, readErr)
	require.Equal(t, original, after)
}

// TestInitForceOverwrites requires that -force overwrites an existing
// gen/main.go and says so.
func TestInitForceOverwrites(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("gen", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("gen", "main.go"), []byte("package main\n\n// stale\n"), 0o644))

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-force"}, &out))
	require.Contains(t, out.String(), "overwrote "+filepath.Join("gen", "main.go"))

	after, err := os.ReadFile(filepath.Join("gen", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(after), `Package: "store"`)
	require.NotContains(t, string(after), "// stale")
}

// TestInitRejectsInvalidInput requires that every invalid flag combination
// is refused before anything is written.
func TestInitRejectsInvalidInput(t *testing.T) {
	testCases := map[string][]string{
		"unknown dialect": {"init", "-dialect", "oracle", "-package", "store", "-output", "internal/store"},
		"empty package":   {"init", "-dialect", "sqlite", "-package", "", "-output", "internal/store"},
		"invalid package": {"init", "-dialect", "sqlite", "-package", "1store", "-output", "internal/store"},
		"blank package":   {"init", "-dialect", "sqlite", "-package", "_", "-output", "internal/store"},
		"empty output":    {"init", "-dialect", "sqlite", "-package", "store", "-output", ""},
		"absolute output": {"init", "-dialect", "sqlite", "-package", "store", "-output", "/abs/store"},
		"empty gen-dir":   {"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-gen-dir", ""},
	}
	for name, args := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out bytes.Buffer
			require.Error(t, Run(args, &out))
			_, err := os.Stat("gen")
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

// TestInitCreatesNothingElse requires that a run leaves the working
// directory holding exactly gen/main.go: no output directory, and no
// database file.
func TestInitCreatesNothingElse(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}, &out))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "gen", entries[0].Name())

	_, err = os.Stat("internal")
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestInitPrintsTheNextCommands requires that the printed "go get" line
// names the driver matching -dialect.
func TestInitPrintsTheNextCommands(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "mysql", "-package", "store", "-output", "internal/store"}, &out))

	text := out.String()
	require.Contains(t, text, "wrote "+filepath.Join("gen", "main.go"))
	require.Contains(t, text, "Next:")
	require.Contains(t, text, "go get github.com/go-sql-driver/mysql")
	require.Contains(t, text, "DATABASE_URL=... go generate ./...")
}
