package rasqlgen

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
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

// TestInitQuotesTheOutputInStoreDir requires that -output reaches
// Store.Dir as a Go string literal built by strconv.Quote, so no -output
// value can end the literal and have the rest of itself read as Go. The
// crafted values below are invented for this test and appear nowhere in
// the tree. Each case also requires that the scaffold still parses and is
// gofmt-clean, because the whole point of the escape is that a value which
// would have produced *valid* Go -- which is why format.Source cannot
// catch it -- now produces the directory that was actually typed.
func TestInitQuotesTheOutputInStoreDir(t *testing.T) {
	testCases := map[string]string{
		"expression injection":   `internal/store" + os.Getenv("RASQLGEN_INJECTED_ENV"), //`,
		"struct field injection": `internal/store", Hints: injectedHints(), //`,
		"backslash escape":       `internal/sto\x22re`,
		"bare double quote":      `internal/sto"re`,
		// A backtick is legal in a Go interpreted string literal and legal
		// in a directory name, so it is carried through, not rejected.
		"backtick": "internal/sto`re",
	}
	for name, output := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out bytes.Buffer
			require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", output}, &out))

			source, err := os.ReadFile(filepath.Join("gen", "main.go"))
			require.NoError(t, err)
			text := string(source)

			require.Contains(t, text, "Dir:     "+strconv.Quote(output)+",")

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "main.go", source, parser.ParseComments)
			require.NoError(t, err)
			formatted, err := format.Source(source)
			require.NoError(t, err)
			require.Equal(t, string(formatted), text)

			// Reading the generate.Store literal back through the parser is
			// what proves the escape held. Dir has to be one string literal
			// whose value is the directory typed -- an -output that closed
			// the literal would leave a concatenation or another expression
			// there instead -- and the field list has to be exactly the one
			// the template writes, since the same trick injects extra
			// fields.
			fields, dir := storeLiteral(t, file)
			require.Equal(t, output, dir)
			require.Equal(t, []string{"Package", "Dir", "Tables", "Hints", "Dialect", "Queries", "Prune"}, fields)
		})
	}
}

// storeLiteral returns the field names of the generate.Store composite
// literal the scaffold writes, in source order, and the unquoted value of
// its Dir field. It fails the test if Dir is anything other than a single
// string literal.
func storeLiteral(t *testing.T, file *ast.File) ([]string, string) {
	t.Helper()
	var fields []string
	var dir string
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		composite, isComposite := node.(*ast.CompositeLit)
		if !isComposite {
			return true
		}
		selector, isSelector := composite.Type.(*ast.SelectorExpr)
		if !isSelector || selector.Sel.Name != "Store" {
			return true
		}
		for _, element := range composite.Elts {
			keyValue, isKeyValue := element.(*ast.KeyValueExpr)
			require.Truef(t, isKeyValue, "generate.Store element is not keyed: %s", types.ExprString(element))
			key, isIdent := keyValue.Key.(*ast.Ident)
			require.Truef(t, isIdent, "generate.Store key is not an identifier: %s", types.ExprString(keyValue.Key))
			fields = append(fields, key.Name)
			if key.Name != "Dir" {
				continue
			}
			literal, isLiteral := keyValue.Value.(*ast.BasicLit)
			require.Truef(t, isLiteral, "Store.Dir is not a literal: %s", types.ExprString(keyValue.Value))
			require.Equal(t, token.STRING, literal.Kind)
			value, err := strconv.Unquote(literal.Value)
			require.NoError(t, err)
			dir = value
		}
		found = true
		return false
	})
	require.True(t, found, "no generate.Store literal in the scaffold")
	return fields, dir
}

// TestInitRejectsControlCharactersInOutput requires that an -output holding
// an ASCII control character is refused before anything is written. The
// rule is the blanket one runInit applies -- every character below 0x20,
// plus DEL -- and not a rule about what breaks the generated file, so the
// cases below are deliberately not all line-breaking: a tab, a carriage
// return and a DEL each produce a scaffold that compiles, and only the
// newline ends the doc comment line -output is named raw on. What the
// blanket rule buys is that unprintable text never reaches the comment or
// the source in the first place.
func TestInitRejectsControlCharactersInOutput(t *testing.T) {
	testCases := map[string]string{
		"newline":         "internal/sto\nre",
		"carriage return": "internal/sto\rre",
		"tab":             "internal/sto\tre",
		"nul":             "internal/sto\x00re",
		"delete":          "internal/sto\x7fre",
	}
	for name, output := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out bytes.Buffer
			err := Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", output}, &out)
			require.ErrorContains(t, err, "control character")
			_, statErr := os.Stat("gen")
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

// TestInitRefusesOutputInTheScaffoldDirectory requires that an -output
// naming the same directory as -gen-dir is refused before anything is
// written. Accepting it is not a cosmetic mistake: the scaffold is package
// main, the store it generates is -package, and the first `go generate
// ./...` therefore leaves a directory holding two packages that no later
// build can compile.
//
// The spellings below are the reason the check resolves both flags instead
// of comparing them as typed. Each names the -gen-dir directory without
// matching it as a string, on either side of the comparison.
func TestInitRefusesOutputInTheScaffoldDirectory(t *testing.T) {
	testCases := map[string]struct {
		output string
		genDir string
	}{
		"plain":            {output: "gen"},
		"dot slash prefix": {output: "./gen"},
		"trailing slash":   {output: "gen/"},
		"parent hop":       {output: "gen/../gen"},
		"gen-dir alias":    {output: "./gen", genDir: "gen/."},
		"non-default":      {output: "tools/generator", genDir: "tools/generator"},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			args := []string{"init", "-dialect", "sqlite", "-package", "store", "-output", testCase.output}
			genDir := "gen"
			if testCase.genDir != "" {
				args = append(args, "-gen-dir", testCase.genDir)
				genDir = testCase.genDir
			}

			var out bytes.Buffer
			err := Run(args, &out)
			require.ErrorContains(t, err, "name the same directory")
			require.ErrorContains(t, err, testCase.output)

			_, statErr := os.Stat(filepath.Clean(genDir))
			require.ErrorIs(t, statErr, os.ErrNotExist)
			require.Empty(t, out.String())
		})
	}
}

// TestInitAcceptsOutputNestedInTheScaffoldDirectory is the control for the
// test above: a subdirectory of -gen-dir is its own package, so only an
// exact match is refused and this layout must still be written.
func TestInitAcceptsOutputNestedInTheScaffoldDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "gen/store"}, &out))

	source, err := os.ReadFile(filepath.Join("gen", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), `Dir:     "gen/store"`)
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

// TestInitScaffoldDocStatesTheForceTerms walks the sequence a user
// actually runs -- init, init again, init -force -- and requires the doc
// comment of the file init just rewrote to describe that sequence rather
// than contradict it. The comment is the only place a reader learns what
// a second run does, so a scaffold claiming it is written once and left
// alone, from a run that had just replaced it, would be read as the
// authority and believed. Each of the three assertions below pins one
// step of the sequence to the sentence covering it.
func TestInitScaffoldDocStatesTheForceTerms(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join("gen", "main.go")

	var first bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}, &first))

	var second bytes.Buffer
	err := Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store"}, &second)
	require.ErrorContains(t, err, "-force")

	var third bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "mysql", "-package", "store", "-output", "internal/store", "-force"}, &third))
	require.Contains(t, third.String(), "overwrote "+path)

	source, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(source), "dialect.MySQL()", "the -force run must have rewritten the body")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	require.NoError(t, err)
	require.NotNil(t, file.Doc, "the scaffold must carry a package doc comment")
	doc := file.Doc.Text()

	require.Contains(t, doc, "-force",
		"the scaffold doc must name the flag that rewrites it, got:\n%s", doc)
	require.Contains(t, doc, "refuses",
		"the scaffold doc must say a plain rerun is refused, got:\n%s", doc)
	require.Contains(t, doc, "discards",
		"the scaffold doc must say -force discards edits made here, got:\n%s", doc)
}

// TestInitRejectsInvalidInput requires that each invalid flag value below
// is refused before anything is written. Two rejections have tests of their
// own rather than a row here, because each needs assertions this table has
// no room for: TestInitRejectsControlCharactersInOutput and
// TestInitRefusesOutputInTheScaffoldDirectory.
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
