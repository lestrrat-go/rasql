package diff

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxSourceFileBytes = 8 << 20
	maxSourceBytes     = 64 << 20
	maxSourceCount     = 1024
)

// LoadSources reads every SQL file in directory and its non-hidden children.
func LoadSources(directory string) ([]Source, error) {
	if directory == "" {
		return nil, fmt.Errorf("migrate diff: schema directory must not be empty")
	}
	sources := make([]Source, 0)
	var totalBytes int64
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
		if len(sources) >= maxSourceCount {
			return fmt.Errorf("schema directory %q exceeds source count limit of %d", directory, maxSourceCount)
		}
		data, err := readSource(path)
		if err != nil {
			return fmt.Errorf("read schema source %q: %w", path, err)
		}
		if totalBytes+int64(len(data)) > maxSourceBytes {
			return fmt.Errorf("schema directory %q exceeds aggregate byte limit of %d bytes", directory, maxSourceBytes)
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return fmt.Errorf("resolve schema source %q: %w", path, err)
		}
		sources = append(sources, Source{Path: filepath.ToSlash(relative), SQL: string(data)})
		totalBytes += int64(len(data))
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

func readSource(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxSourceFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceFileBytes {
		return nil, fmt.Errorf("source exceeds per-file limit of %d bytes", maxSourceFileBytes)
	}
	return data, nil
}
