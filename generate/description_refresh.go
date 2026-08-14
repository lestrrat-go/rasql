package generate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

// ValidateDescriptionPackageOwnership reports whether every entry in
// directory is one `rasqlgen bootstrap` recognizes as its own: either
// descriptionHintsFilename, which it may or may not have written yet, or an
// ordinary file whose first line is genfile.Bootstrap.Marker() -- the same
// mark every other file bootstrap writes carries. A refresh calls this
// before reading or changing anything, so it only ever runs against a
// directory it created, on the same terms WriteDescriptionPackage already
// enforces for a first run: refusing a hand-written file, or output from
// another generator, that happens to sit in -output.
func ValidateDescriptionPackageOwnership(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			return fmt.Errorf("bootstrap output %q holds a subdirectory %q it did not create; refusing to refresh a directory it does not own", directory, name)
		}
		if name == descriptionHintsFilename {
			continue
		}
		path := filepath.Join(directory, name)
		if err := requireGeneratedMarker(path); err != nil {
			return fmt.Errorf("bootstrap output %q holds %q, which it did not write; refusing to refresh a directory it does not own: %w", directory, name, err)
		}
	}
	return nil
}

// requireGeneratedMarker reports an error unless path's first line is
// exactly genfile.Bootstrap.Marker().
func requireGeneratedMarker(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	line = strings.TrimRight(line, "\r\n")
	if line != genfile.Bootstrap.Marker() {
		return fmt.Errorf("its first line is not %q", genfile.Bootstrap.Marker())
	}
	return nil
}

// ApplyDescriptionDiff applies diff, which DiffDescriptionPackage must have
// computed against directory's current Tables() and tables, the same fresh
// inspection: it writes the file for every added or changed table, deletes
// the file for every removed table, and rewrites the aggregator so
// directory's Tables() returns exactly tables afterward. hints.go is
// written only if it does not exist yet, on the same terms
// WriteDescriptionPackage writes it, and is otherwise left untouched --
// see internal/schemagen.HintsSource.
//
// An empty diff is a no-op: nothing is read or written, so a caller can
// call this unconditionally after a report finds no drift without
// disturbing the package's mtimes or file contents.
//
// The caller is responsible for calling ValidateDescriptionPackageOwnership
// first; ApplyDescriptionDiff does not call it itself, so a caller that
// only wants the report -- never write -- never pays for a directory
// listing this function does not otherwise need.
func ApplyDescriptionDiff(packageName, directory string, tables []schema.TableDef, diff DescriptionDiff) error {
	if diff.Empty() {
		return nil
	}

	files, err := prepareDescriptionFiles(tables)
	if err != nil {
		return err
	}
	fileByName := make(map[string]descriptionFile, len(files))
	for _, file := range files {
		fileByName[file.table.Name] = file
	}

	changed := make(map[string]struct{}, len(diff.Added)+len(diff.Changed))
	for _, table := range diff.Added {
		changed[table.Name] = struct{}{}
	}
	for _, table := range diff.Changed {
		changed[table.Name] = struct{}{}
	}
	for name := range changed {
		file, ok := fileByName[name]
		if !ok {
			return fmt.Errorf("bootstrap refresh: table %q from the diff is missing from the current inspection", name)
		}
		source, err := schemagen.DescriptionSource(packageName, file.funcName, file.table)
		if err != nil {
			return err
		}
		if err := genfile.Write(genfile.Bootstrap, filepath.Join(directory, file.filename), source); err != nil {
			return fmt.Errorf("write table %q: %w", name, err)
		}
	}

	for _, table := range diff.Removed {
		path := filepath.Join(directory, schemaOutputFilename(table.Name))
		if err := requireGeneratedMarker(path); err != nil {
			return fmt.Errorf("refusing to delete %q for dropped table %q: %w", path, table.Name, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete table %q: %w", table.Name, err)
		}
	}

	orderedFuncNames := make([]string, len(files))
	for index, file := range files {
		orderedFuncNames[index] = file.funcName
	}
	aggregatorSource, err := schemagen.TablesFuncSource(packageName, orderedFuncNames)
	if err != nil {
		return err
	}
	if err := genfile.Write(genfile.Bootstrap, filepath.Join(directory, descriptionAggregatorFilename), aggregatorSource); err != nil {
		return fmt.Errorf("write %s: %w", descriptionAggregatorFilename, err)
	}

	if err := writeHintsFileIfAbsent(packageName, directory); err != nil {
		return fmt.Errorf("write %s: %w", descriptionHintsFilename, err)
	}
	return nil
}
