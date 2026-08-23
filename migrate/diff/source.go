package diff

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
)

const (
	maxSourceFileBytes = 8 << 20
	maxSourceBytes     = 64 << 20
	maxSourceCount     = 1024
)

// SourcesFromTable renders one inspected table as desired-schema sources.
// The dialect package selects the dialect; this shared helper only preserves
// the source layout used by diff-live.
func SourcesFromTable(d dialect.Dialect, table schema.TableDef) ([]Source, error) {
	created, err := render.CreateTable(d, table)
	if err != nil {
		return nil, fmt.Errorf("render inspected table %q: %w", table.QualifiedName(), err)
	}
	sources := []Source{{Path: "live/" + table.Name + ".sql", SQL: sqltext.Text(created.SQL() + ";\n")}}
	indexes, err := render.CreateIndexes(d, table)
	if err != nil {
		return nil, fmt.Errorf("render indexes for inspected table %q: %w", table.QualifiedName(), err)
	}
	for index, statement := range indexes {
		sources = append(sources, Source{
			Path: fmt.Sprintf("live/index_%d.sql", index+1),
			SQL:  sqltext.Text(statement.SQL() + ";\n"),
		})
	}
	return sources, nil
}

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
		remainingBytes := maxSourceBytes - totalBytes
		data, err := readSource(path, remainingBytes)
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
		sources = append(sources, Source{Path: filepath.ToSlash(relative), SQL: sqltext.Text(data)})
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

func readSource(path string, remainingBytes int64) (data []byte, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	readLimit := int64(maxSourceFileBytes)
	if remainingBytes < readLimit {
		readLimit = remainingBytes
	}
	data, err = io.ReadAll(io.LimitReader(file, readLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceFileBytes {
		return nil, fmt.Errorf("source exceeds per-file limit of %d bytes", maxSourceFileBytes)
	}
	return data, nil
}
