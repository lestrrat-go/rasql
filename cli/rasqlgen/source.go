package rasqlgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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

	// The pattern is what `go list` is asked to resolve; sourceDir is what
	// the user typed, and stays the name every message reports back.
	importPath, moduleDir, err := resolveSchemaSourcePackage(ctx, schemaSourcePattern(sourceDir), sourceDir)
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
	cmd := exec.CommandContext(ctx, "go", "run", "./"+filepath.Base(temporaryDir))
	cmd.Dir = moduleDir
	cmd.Stdout = writer
	cmd.Stderr = writer
	// writer is os.Stderr for the command itself, which exec hands to the
	// child as a file descriptor. Any other writer makes exec create a
	// pipe and wait for every holder of its write end to close, and a
	// process the child leaves behind holds one; WaitDelay bounds that
	// wait so a cancelled run still reaches the cleanup above promptly.
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		// The detail already reached writer through the child's own
		// stderr; this wrapper stays short rather than repeating it.
		return fmt.Errorf("schema source %s: %w", importPath, err)
	}
	return nil
}

// schemaSourcePattern returns the package pattern `go list` should resolve
// for a -source value. Unlike loadExistingDescriptionTables, which is
// handed a directory by its own caller, -source is typed at the command
// line and accepts either form: the documented directory ("internal/tables")
// and a package import path ("example.com/app/internal/tables").
//
// Only the directory form gets asDirectoryPattern's "./" prefix, because
// that prefix is what stops go list from reading a bare relative directory
// as an import-path pattern to match against the whole module graph.
// Prefixing an import path instead resolves whatever directory happens to
// sit at that same relative path, or fails with "directory not found" when
// none does, so an import path must reach go list untouched.
//
// Three tests in order decide which form a value is, and the first two
// settle it without looking at the filesystem at all:
//
//   - A value isDirectoryPattern already reports to be in explicit
//     directory form is left alone, which asDirectoryPattern does itself.
//     That form is the escape hatch for a directory whose own name would
//     otherwise read as an import path.
//   - A value whose first path element carries a dot is a package import
//     path. A dot there is the go command's own test for the same thing:
//     IsStandardImportPath in cmd/go/internal/search assumes "code will
//     start with a domain name (dot in the first element)" and treats a
//     path without one as standard-library. Deciding this from the value alone rather than
//     from os.Stat is what keeps an import path resolving as itself even
//     when a directory of exactly that relative path sits under the
//     working directory.
//   - Anything else is the documented directory form when it names a
//     directory on disk, and is otherwise passed through for go list to
//     resolve as the standard-library-shaped import path it looks like.
func schemaSourcePattern(source string) string {
	if isSchemaSourceImportPath(source) {
		return source
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return source
	}
	return asDirectoryPattern(source)
}

// isSchemaSourceImportPath reports whether source can only be a package
// import path, never the documented -source directory form. A value already
// written in explicit directory form is never one, whatever it is named;
// past that, a dot in the first path element marks the leading domain of a
// module path. A directory really named that way is still reachable, by
// writing it in the explicit form.
func isSchemaSourceImportPath(source string) bool {
	if isDirectoryPattern(source) {
		return false
	}
	firstElement, _, _ := strings.Cut(source, "/")
	return strings.Contains(firstElement, ".")
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

// resolveSchemaSourcePackage resolves pattern to an import path and its
// module's root directory, running with the current process's own working
// directory so a relative pattern is read the way the user typed it.
//
// pattern is the package pattern go list is asked to resolve, which a
// caller may have rewritten from what the user typed. source is that
// original value, and is the only one of the two any message here reports,
// so an error names what the user wrote rather than a rewrite of it.
//
// go list -json is a resolver only, not an error gate: measured on
// go1.26.1, it exits 0 on a type error, a truncated file, and an
// unresolvable import in the package it resolves, reporting those only
// inside the JSON. Only a missing directory or similarly unresolvable
// pattern exits non-zero with a message on stderr, which is what this
// reports. A compile failure in the package itself is caught later, when
// generateFromSchemaSource runs `go run` on the generated program that
// imports it.
//
// It runs under ctx so a signal arriving during resolution stops this
// command as promptly as one arriving during the child run does.
func resolveSchemaSourcePackage(ctx context.Context, pattern, source string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-json", pattern)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimRight(stderr.String(), "\n")
		if message == "" {
			// go list wrote nothing to stderr, which happens when the
			// child never ran at all -- most commonly because "go" is not
			// on PATH. err itself names that failure; reporting it instead
			// of an empty errors.New("") is what keeps this branch from
			// handing the user a message with nothing after the colon.
			return "", "", fmt.Errorf("resolve schema source %s: %w", source, err)
		}
		return "", "", fmt.Errorf("resolve schema source %s: %w", source, errors.New(message))
	}

	var pkg schemaSourcePackageInfo
	if err := json.Unmarshal(stdout.Bytes(), &pkg); err != nil {
		return "", "", fmt.Errorf("resolve schema source %s: %w", source, err)
	}
	if pkg.Module.Dir == "" {
		return "", "", fmt.Errorf("schema source %s is not inside a Go module", source)
	}
	if pkg.ImportPath == "" {
		return "", "", fmt.Errorf("resolve schema source %s: %w", source, errors.New("go list reported no import path"))
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
