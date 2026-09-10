// Package scratchmod builds the go.mod a test fixture needs when it builds
// a scratch Go module in a temporary directory to prove generated code
// compiles and behaves.
//
// It parses the repository's own go.mod with golang.org/x/mod/modfile and
// edits it through modfile's typed operations, rather than through
// strings.Replace against the module line. strings.Replace reports no
// error and returns its input unchanged when the text it looks for is
// gone, so a fixture built that way can silently keep writing a go.mod
// that still declares itself to BE github.com/lestrrat-go/rasql while also
// carrying a replace directive back to it -- a state the go tool then
// rejects in a confusing way, far from the line that caused it.
// modfile.AddModuleStmt, AddRequire and AddReplace report an error instead,
// and AddReplace updates an existing replace directive rather than
// appending a duplicate.
package scratchmod

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// ForModule parses the go.mod at repoRoot -- this repository's own -- and
// returns an editable copy renamed to modulePath, with a require and a
// replace directive added for the repository's own module path pointing
// back at repoRoot. This is the shape every scratch fixture needs: a
// module that can import the repository's packages under a different
// import path while still building against the repository's own source.
//
// The returned file is not yet written anywhere. A caller with nothing
// further to add can pass it to Format and write the result as go.mod; a
// caller that also needs an extra require or replace (a pinned driver
// version, a second local module) can call the modfile.File's own methods
// on it first.
func ForModule(repoRoot, modulePath string) (*modfile.File, error) {
	goModPath := filepath.Join(repoRoot, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", goModPath, err)
	}
	file, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", goModPath, err)
	}
	repoModulePath := file.Module.Mod.Path
	if err := file.AddModuleStmt(modulePath); err != nil {
		return nil, fmt.Errorf("set module path to %s: %w", modulePath, err)
	}
	if err := file.AddRequire(repoModulePath, "v0.0.0"); err != nil {
		return nil, fmt.Errorf("require %s: %w", repoModulePath, err)
	}
	target := filepath.ToSlash(repoRoot)
	if err := file.AddReplace(repoModulePath, "", target, ""); err != nil {
		return nil, fmt.Errorf("replace %s => %s: %w", repoModulePath, target, err)
	}
	return file, nil
}

// Format cleans up f and renders it back to go.mod bytes, the formatting
// step golang.org/x/mod/modfile.Format performs. It is wrapped here so a
// caller that only reaches a *modfile.File through this package's
// functions never needs its own import of golang.org/x/mod/modfile.
func Format(f *modfile.File) ([]byte, error) {
	f.Cleanup()
	return modfile.Format(f.Syntax), nil
}

// Write builds the go.mod ForModule describes and writes it, alongside a
// copy of the repository's go.sum, into dir. This is the common case: a
// fixture with no dependency beyond the repository itself. A fixture that
// needs an extra require or replace directive should call ForModule, edit
// the returned file, then Format and WriteGoSum itself.
func Write(dir, repoRoot, modulePath string) error {
	file, err := ForModule(repoRoot, modulePath)
	if err != nil {
		return err
	}
	data, err := Format(file)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Join(dir, "go.mod"), err)
	}
	return WriteGoSum(dir, repoRoot)
}

// WriteGoSum copies the repository's go.sum into dir, so a scratch
// module's dependency graph resolves without reaching a module proxy.
func WriteGoSum(dir, repoRoot string) error {
	goSumPath := filepath.Join(repoRoot, "go.sum")
	data, err := os.ReadFile(goSumPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", goSumPath, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Join(dir, "go.sum"), err)
	}
	return nil
}

// Repoint parses the go.mod at goModPath, updates its existing replace
// directive for modulePath to point at newTarget, and writes the file back
// with its original permissions.
//
// It serves a fixture that starts from an already-checked-in go.mod -- one
// with its own module path and its own replace directive back to this
// repository -- and only needs that replace directive repointed once the
// go.mod has been copied somewhere else. Unlike ForModule, it does not
// rename the module or add a require: the checked-in go.mod already has
// both.
func Repoint(goModPath, modulePath, newTarget string) error {
	info, err := os.Stat(goModPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", goModPath, err)
	}
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", goModPath, err)
	}
	file, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return fmt.Errorf("parse %s: %w", goModPath, err)
	}
	if err := file.AddReplace(modulePath, "", newTarget, ""); err != nil {
		return fmt.Errorf("replace %s => %s: %w", modulePath, newTarget, err)
	}
	out, err := Format(file)
	if err != nil {
		return err
	}
	if err := os.WriteFile(goModPath, out, info.Mode()); err != nil {
		return fmt.Errorf("write %s: %w", goModPath, err)
	}
	return nil
}
