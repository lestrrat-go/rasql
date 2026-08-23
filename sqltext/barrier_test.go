package sqltext_test

// This file proves the one property sqltext exists for: an untyped string
// constant converts to sqltext.Text with no ceremony, and a string variable
// built at run time does not compile without an explicit conversion. The
// positive half below is ordinary compiled code -- if it compiles, the first
// part of the property holds. The negative half needs a build that must
// fail, so it builds a scratch module and asserts that failure; see
// TestTextRejectsARuntimeStringWithoutConversion.
//
// Three call sites share this one test rather than being split across
// statement and schema: statement.New, schema.Default and schema.Check all
// take a sqltext.Text parameter, and one scratch module with one main.go
// checks all three in a single "go build", which is cheaper than three
// scratch modules making the same point three times.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/statement"
	"github.com/stretchr/testify/require"
)

// The positive half: an untyped string constant converts to sqltext.Text
// implicitly, at every call site the barrier protects.
var (
	_ = statement.New("SELECT 1")
	_ = schema.Default("CURRENT_TIMESTAMP")
	_ = schema.Check("balance >= 0")
)

// TestTextRejectsARuntimeStringWithoutConversion is the negative half: a
// string built at run time must not compile against a sqltext.Text
// parameter without an explicit sqltext.Text(...) conversion. The compiler
// enforces this, so the only way to test it is to compile code that must
// fail and check that it did.
func TestTextRejectsARuntimeStringWithoutConversion(t *testing.T) {
	moduleDir := t.TempDir()

	repoGoMod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	require.NoError(t, err)
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/barrier\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(module), 0o600))

	repoGoSum, err := os.ReadFile(filepath.Join("..", "go.sum"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.sum"), repoGoSum, 0o600))

	const mainSource = `package main

import (
	"os"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/statement"
)

func main() {
	built := os.Getenv("BARRIER_TEST_SQL")

	_ = statement.New(built)
	_ = schema.Default(built)
	_ = schema.Check(built)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte(mainSource), 0o600))

	// -buildvcs=false: the scratch module lives in t.TempDir(), which is
	// never itself a VCS checkout, but Go's VCS auto-detection has been
	// observed to still try (and fail) to run "git status" against it. The
	// scratch module needs no VCS stamp regardless, so this sidesteps the
	// detection rather than depending on it correctly deciding there is
	// nothing to stamp.
	command := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "./...")
	command.Dir = moduleDir
	command.Env = append(os.Environ(), "GOPROXY=off")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()

	require.Errorf(t, err, "a string variable must not compile against a sqltext.Text parameter without an explicit conversion, but the build succeeded:\n%s", output.String())
	require.Contains(t, output.String(), "cannot use")
	require.Contains(t, output.String(), "statement.New", "the statement.New call site must be named in the build failure")
	require.Contains(t, output.String(), "schema.Default", "the schema.Default call site must be named in the build failure")
	require.Contains(t, output.String(), "schema.Check", "the schema.Check call site must be named in the build failure")
}
