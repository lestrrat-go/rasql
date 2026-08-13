package rasqlgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// runSchemaSource implements `rasqlgen schema -source`. It resolves
// sourceDir to a package inside the caller's Go module, writes a temporary
// package main into that module that imports the schema package and calls
// generate.WritePackage itself, runs it with `go run`, and removes the
// temporary directory on both the success and the failure path.
//
// The child writes the generated files itself: nothing is serialized back
// to this process, and the child's combined stdout and stderr are forwarded
// to writer as they are produced.
func runSchemaSource(sourceDir, packageName, outputDir string, tableNames []string, writer io.Writer) error {
	// The output path must be made absolute before anything changes
	// directory: -output is relative to the user's invocation cwd, but the
	// child process below runs with its cwd at the module root.
	absOutput, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve schema output %s: %w", outputDir, err)
	}
	info, err := os.Stat(absOutput)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("schema output %q is not a directory", outputDir)
	}

	importPath, moduleDir, err := resolveSchemaSourcePackage(sourceDir)
	if err != nil {
		return err
	}

	// The dot prefix is load-bearing: go list ./... and a concurrent go
	// build ./... both skip a dot-prefixed directory, while a plain one
	// would be picked up mid-write. MkdirTemp guarantees a name no
	// concurrent run of this command already holds.
	temporaryDir, err := os.MkdirTemp(moduleDir, ".rasqlgen-source-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporaryDir) }()

	program := schemaSourceProgram(importPath, packageName, absOutput, tableNames)
	if err := os.WriteFile(filepath.Join(temporaryDir, "main.go"), program, 0o600); err != nil {
		return err
	}

	// The argument is joined with a literal "./" rather than filepath.Join
	// so it stays a relative package pattern on every platform, built from
	// only the temporary directory's base name.
	cmd := exec.Command("go", "run", "./"+filepath.Base(temporaryDir))
	cmd.Dir = moduleDir
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Run(); err != nil {
		// The detail already reached writer through the child's own
		// stderr; this wrapper stays short rather than repeating it.
		return fmt.Errorf("schema source %s: %w", importPath, err)
	}
	return nil
}

// schemaSourcePackageInfo holds the fields `go list -json` reports that
// resolveSchemaSourcePackage needs: the package's import path and its
// module's root directory.
type schemaSourcePackageInfo struct {
	ImportPath string
	Module     struct {
		Path string
		Dir  string
	}
}

// resolveSchemaSourcePackage resolves sourceDir to an import path and its
// module's root directory, running with the current process's own working
// directory so a relative sourceDir is read the way the user typed it.
//
// go list -json is a resolver only, not an error gate: measured on
// go1.26.1, it exits 0 on a type error, a truncated file, and an
// unresolvable import in the package it resolves, reporting those only
// inside the JSON. Only a missing directory or similarly unresolvable
// sourceDir exits non-zero with a message on stderr, which is what this
// reports. A compile failure in the package itself is caught later, when
// runSchemaSource runs `go run` on the generated program that imports it.
func resolveSchemaSourcePackage(sourceDir string) (string, string, error) {
	cmd := exec.Command("go", "list", "-json", sourceDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimRight(stderr.String(), "\n")
		return "", "", fmt.Errorf("resolve schema source %s: %w", sourceDir, errors.New(message))
	}

	var pkg schemaSourcePackageInfo
	if err := json.Unmarshal(stdout.Bytes(), &pkg); err != nil {
		return "", "", fmt.Errorf("resolve schema source %s: %w", sourceDir, err)
	}
	if pkg.Module.Dir == "" {
		return "", "", fmt.Errorf("schema source %s is not inside a Go module", sourceDir)
	}
	if pkg.ImportPath == "" {
		return "", "", fmt.Errorf("resolve schema source %s: %w", sourceDir, errors.New("go list reported no import path"))
	}
	return pkg.ImportPath, pkg.Module.Dir, nil
}

// schemaSourceProgram builds the temporary package main that
// runSchemaSource writes, runs, and deletes. It is built with a
// bytes.Buffer and strconv.Quote for every baked-in value rather than
// fmt.Sprintf, because a format template would force every literal '%' the
// program's own error messages use (such as filterTables's %q) to be
// escaped as '%%'.
//
// The schema package is imported under the alias schemasource, so this
// program never needs to know the user's own package name and a directory
// whose package name disagrees with its directory name still works.
//
// The program need not be gofmt-clean: it is written, run, and deleted, and
// nothing formats or vets it. It is kept readable anyway, because a compile
// error in the user's schema package quotes lines from this file back to
// the user.
func schemaSourceProgram(importPath, packageName, outputDir string, tableNames []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by rasqlgen; DO NOT EDIT.\n\n")
	buf.WriteString("package main\n\n")
	buf.WriteString("import (\n")
	buf.WriteString("\t\"fmt\"\n")
	buf.WriteString("\t\"os\"\n\n")
	buf.WriteString("\tschemasource " + strconv.Quote(importPath) + "\n")
	buf.WriteString("\t\"github.com/lestrrat-go/rasql/generate\"\n")
	buf.WriteString("\t\"github.com/lestrrat-go/rasql/schema\"\n")
	buf.WriteString(")\n\n")
	buf.WriteString("func main() {\n")
	buf.WriteString("\ttables := schemasource.Tables()\n")
	buf.WriteString("\tif len(tables) == 0 {\n")
	buf.WriteString("\t\tfail(fmt.Errorf(\"schema source %s returned no tables\", " + strconv.Quote(importPath) + "))\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\ttables, err := filterTables(tables, []string{" + quotedStringList(tableNames) + "})\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\tfail(err)\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif err := generate.WritePackage(" + strconv.Quote(packageName) + ", " + strconv.Quote(outputDir) + ", tables...); err != nil {\n")
	buf.WriteString("\t\tfail(err)\n")
	buf.WriteString("\t}\n")
	buf.WriteString("}\n\n")
	buf.WriteString("func fail(err error) {\n")
	buf.WriteString("\tfmt.Fprintln(os.Stderr, err)\n")
	buf.WriteString("\tos.Exit(1)\n")
	buf.WriteString("}\n\n")
	buf.WriteString("func filterTables(tables []schema.TableDef, names []string) ([]schema.TableDef, error) {\n")
	buf.WriteString("\tif len(names) == 0 {\n")
	buf.WriteString("\t\treturn tables, nil\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\trequested := make(map[string]struct{}, len(names))\n")
	buf.WriteString("\tfor _, name := range names {\n")
	buf.WriteString("\t\trequested[name] = struct{}{}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tfiltered := make([]schema.TableDef, 0, len(tables))\n")
	buf.WriteString("\tfound := make(map[string]struct{}, len(names))\n")
	buf.WriteString("\tfor _, table := range tables {\n")
	buf.WriteString("\t\tif _, ok := requested[table.Name]; !ok {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tfiltered = append(filtered, table)\n")
	buf.WriteString("\t\tfound[table.Name] = struct{}{}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tfor _, name := range names {\n")
	buf.WriteString("\t\tif _, ok := found[name]; !ok {\n")
	buf.WriteString("\t\t\treturn nil, fmt.Errorf(\"schema source has no table %q\", name)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn filtered, nil\n")
	buf.WriteString("}\n")
	return buf.Bytes()
}

// quotedStringList renders names as a comma-separated list of Go string
// literals, suitable for splicing into a []string{...} composite literal.
func quotedStringList(names []string) string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = strconv.Quote(name)
	}
	return strings.Join(quoted, ", ")
}
