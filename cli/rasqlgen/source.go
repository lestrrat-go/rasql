package rasqlgen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/lestrrat-go/rasql/internal/sourcepkg"
)

// runSchemaSource implements `rasqlgen schema -source`. It handles SIGINT
// and SIGTERM for the duration of the run and delegates the work to
// generateFromSchemaSource, which owns the temporary directory and its
// cleanup.
//
// Handling those two signals is what makes the cleanup reachable at all.
// Left at their default disposition they terminate the process outright,
// and Go runs no deferred function on the way out, so an interrupted run
// would leave its temporary directory inside the user's module. Handled,
// the signal becomes an ordinary context cancellation that returns through
// the existing defer.
func runSchemaSource(sourceDir, packageName, outputDir string, tableNames []string, writer io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// NotifyContext reports only that a signal arrived, never which one,
	// and the signal has to be named again to re-raise it below. A second
	// registration records it: the signal package delivers to every
	// channel registered for that signal, so this one does not compete
	// with the context's own.
	received := make(chan os.Signal, 1)
	signal.Notify(received, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(received)

	err := generateFromSchemaSource(ctx, sourceDir, packageName, outputDir, tableNames, writer)
	if ctx.Err() == nil {
		return err
	}

	// Only reached once generateFromSchemaSource has returned, so its
	// cleanup defer has already removed the temporary directory. The
	// signal is then re-raised with its default disposition, which is
	// what keeps the conventional status for an interrupted process (130
	// for SIGINT, 143 for SIGTERM) instead of replacing it with this
	// command's own exit 1. Cleanup comes first either way: on a platform
	// that will not deliver the signal to this process, the call below
	// returns and err is reported the ordinary way.
	select {
	case sig := <-received:
		raiseWithDefaultDisposition(sig)
	default:
	}
	return err
}

// raiseWithDefaultDisposition restores sig's default disposition and sends
// it to this process. On a Unix system the signal is delivered before the
// send returns and the process terminates there, so nothing after the call
// runs. On a system that rejects the send, it returns and leaves the caller
// to report the run's error instead.
func raiseWithDefaultDisposition(sig os.Signal) {
	signal.Reset(sig)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		return
	}
	_ = process.Signal(sig)
}

// generateFromSchemaSource resolves sourceDir to a package inside the
// caller's Go module, writes a temporary package main into that module that
// imports the schema package and calls generate.WritePackage itself, runs
// it with `go run`, and removes the temporary directory on both the success
// and the failure path.
//
// The child writes the generated files itself: nothing is serialized back
// to this process, and the child's combined stdout and stderr are forwarded
// to writer as they are produced.
//
// Cancelling ctx stops the child and returns, so the cleanup below runs on
// that path too.
func generateFromSchemaSource(ctx context.Context, sourceDir, packageName, outputDir string, tableNames []string, writer io.Writer) error {
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

	pkg, err := sourcepkg.Resolve(ctx, sourceDir)
	if err != nil {
		return err
	}

	program := schemaSourceProgram(pkg.ImportPath, packageName, absOutput, tableNames)
	if err := pkg.Stream(ctx, ".rasqlgen-source-", program, writer); err != nil {
		// The detail already reached writer through the child's own
		// stderr; this wrapper stays short rather than repeating it.
		return fmt.Errorf("schema source %s: %w", pkg.ImportPath, err)
	}
	return nil
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
