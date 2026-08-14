// Package sourcepkg resolves a directory to a Go package inside the user's
// module and runs a throwaway program in that module against it.
//
// rasqlgen needs this because a user's schema package is not known at
// rasqlgen's own build time: the only way to call its Tables() is to write
// a program that imports it, compile that program inside the user's
// module, and run it.
package sourcepkg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Package is a Go package resolved on disk, together with the root
// directory of the module that holds it.
type Package struct {
	ImportPath string
	ModuleDir  string
}

// packageInfo holds the fields `go list -json` reports that Resolve needs:
// the package's import path and its module's root directory.
type packageInfo struct {
	ImportPath string
	Module     struct {
		Path string
		Dir  string
	}
}

// Resolve resolves directory to a package with `go list -json`, running
// with the current process's working directory so a relative directory is
// read the way the user typed it.
//
// directory is normalized to the one form `go list` always treats as a
// filesystem path rather than an import-path pattern: it is prefixed with
// "./" unless it is already absolute or already begins with "./" or "../".
// Resolving a directory pattern loads only the directory named, where
// resolving an import-path pattern loads the whole module graph and fails
// in a module that carries no go.sum.
//
// `go list -json` is a resolver, not an error gate: it exits 0 on a type
// error or an unresolvable import inside the package it resolves and
// reports those only in the JSON. A compile failure in the package itself
// surfaces later, when Stream or Capture runs the program that imports it.
func Resolve(ctx context.Context, directory string) (Package, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-json", asDirectoryPattern(directory))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimRight(stderr.String(), "\n")
		return Package{}, fmt.Errorf("resolve %s: %w", directory, errors.New(message))
	}

	var info packageInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return Package{}, fmt.Errorf("resolve %s: %w", directory, err)
	}
	if info.Module.Dir == "" {
		return Package{}, fmt.Errorf("%s is not inside a Go module", directory)
	}
	if info.ImportPath == "" {
		return Package{}, fmt.Errorf("resolve %s: %w", directory, errors.New("go list reported no import path"))
	}
	return Package{ImportPath: info.ImportPath, ModuleDir: info.Module.Dir}, nil
}

// asDirectoryPattern returns path in the one form `go list` always resolves
// as a filesystem directory rather than an import-path pattern: unchanged
// if it is already absolute or already starts with "./" or "../", and
// prefixed with "./" otherwise. Go's own package-pattern rule (see `go help
// packages`) treats a bare relative path such as "internal/tables" as an
// import path to match against the module's declared packages, not as a
// directory -- which still resolves the right package by coincidence when
// the two happen to be equal, but takes a different, slower path through
// the module graph to get there, one that this repository's own indirect
// dependencies make fail outright in a scratch module that carries no
// go.sum: resolving an import-path pattern loads the whole module graph,
// where resolving a directory pattern loads only the one directory named.
func asDirectoryPattern(path string) string {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "."+string(filepath.Separator)) || strings.HasPrefix(path, ".."+string(filepath.Separator)) || path == "." || path == ".." {
		return path
	}
	return "." + string(filepath.Separator) + path
}

// writeTemporaryProgram creates a fresh directory under p.ModuleDir named
// with prefix and writes source into it as main.go, returning the
// directory's path.
//
// The dot prefix on the returned directory's name is load-bearing: `go
// list ./...` and a concurrent `go build ./...` both skip a dot-prefixed
// directory, while a plain one would be picked up mid-write. MkdirTemp
// guarantees a name no concurrent run of the caller already holds.
func (p Package) writeTemporaryProgram(prefix string, source []byte) (string, error) {
	temporaryDir, err := os.MkdirTemp(p.ModuleDir, prefix)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporaryDir, "main.go"), source, 0o600); err != nil {
		_ = os.RemoveAll(temporaryDir)
		return "", err
	}
	return temporaryDir, nil
}

// runCommand builds the `go run` invocation for the program writeTemporaryProgram
// left in temporaryDir.
//
// The argument is joined with a literal "./" rather than filepath.Join so
// it stays a relative package pattern on every platform, built from only
// the temporary directory's base name.
func (p Package) runCommand(ctx context.Context, temporaryDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", "run", "./"+filepath.Base(temporaryDir))
	cmd.Dir = p.ModuleDir
	// WaitDelay bounds how long cmd.Run waits for the child's stdout and
	// stderr pipes to close once the child itself has exited, which
	// matters whenever a process the child leaves behind still holds a
	// write end open; without it, a cancelled ctx would not reach the
	// caller's cleanup promptly.
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// Stream writes source as main.go into a temporary directory under
// p.ModuleDir, runs it with `go run`, forwards the program's combined
// stdout and stderr to output as they are produced, and removes the
// temporary directory on every path out, including a cancelled ctx.
//
// prefix names that temporary directory and must begin with a dot: `go
// list ./...` and `go build ./...` both skip a dot-prefixed directory,
// while a plain one would be picked up mid-write.
func (p Package) Stream(ctx context.Context, prefix string, source []byte, output io.Writer) error {
	temporaryDir, err := p.writeTemporaryProgram(prefix, source)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporaryDir) }()

	cmd := p.runCommand(ctx, temporaryDir)
	// output is os.Stderr for the command itself, which exec hands to the
	// child as a file descriptor. Any other writer makes exec create a
	// pipe and wait for every holder of its write end to close, and a
	// process the child leaves behind holds one; WaitDelay above bounds
	// that wait so a cancelled run still reaches the cleanup here promptly.
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}

// Capture is Stream for a program that reports its result rather than
// doing the work itself: it returns what the program wrote to stdout, and
// folds what it wrote to stderr into the error when it fails.
func (p Package) Capture(ctx context.Context, prefix string, source []byte) ([]byte, error) {
	temporaryDir, err := p.writeTemporaryProgram(prefix, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(temporaryDir) }()

	cmd := p.runCommand(ctx, temporaryDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := stderr.String()
		if message == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return stdout.Bytes(), nil
}
