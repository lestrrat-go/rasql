// Package modroot locates the module root a relative generated path is
// resolved against.
//
// It exists so two callers cannot drift apart. generate.Store resolves a
// relative Store.Dir against the module root whenever Store.Root is empty,
// and rasqlgen reaches that same directory to find the settings file a run
// reads when -config names none. A second copy of the walk in the command
// would look for that file beside a directory the generator does not write
// to, which is exactly the disagreement this package removes.
package modroot

import (
	"fmt"
	"os"
	"path/filepath"
)

// FromWorkingDirectory returns the module root for the process working
// directory. It reports an empty string, not an error, when no go.mod is
// found: whether that matters depends on whether a relative path ever needs
// a base to resolve against, which the caller decides.
func FromWorkingDirectory() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	return From(wd), nil
}

// From walks upward from start and returns the first directory holding a
// go.mod file, or an empty string when it reaches the top without finding
// one. A go.mod that is itself a directory is not a module file and does
// not stop the walk.
func From(start string) string {
	dir := start
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
