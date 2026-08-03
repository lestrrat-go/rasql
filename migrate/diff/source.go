package diff

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadSources reads every SQL file in directory and its non-hidden children.
func LoadSources(directory string) ([]Source, error) {
	if directory == "" {
		return nil, fmt.Errorf("migrate diff: schema directory must not be empty")
	}
	sources := make([]Source, 0)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(entry.Name()) != ".sql" {
			return fmt.Errorf("schema directory %q contains non-SQL source %q", directory, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read schema source %q: %w", path, err)
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return fmt.Errorf("resolve schema source %q: %w", path, err)
		}
		sources = append(sources, Source{Path: filepath.ToSlash(relative), SQL: string(data)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("migrate diff: load schema sources: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("migrate diff: schema directory %q has no SQL sources", directory)
	}
	sort.Slice(sources, func(left, right int) bool {
		return sources[left].Path < sources[right].Path
	})
	return sources, nil
}
