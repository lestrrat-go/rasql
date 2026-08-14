package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

// descriptionAggregatorFilename is the one file WriteDescriptionPackage
// writes once per package rather than once per table: the exported func
// Tables() []schema.TableDef that makes directory immediately valid
// `rasqlgen schema -source` input. A table literally named "tables" would
// otherwise generate this same filename through schemaOutputFilename; the
// collision check below rejects that before anything is written.
const descriptionAggregatorFilename = "tables_gen.go"

// descriptionHintsFilename is the user-owned file WriteDescriptionPackage
// writes once, the first time it creates a package, and never rewrites
// again: see internal/schemagen.HintsSource. It deliberately does not end
// in "_gen.go" -- genfile.Write's own suffix rule exists to gate what
// rasqlgen may silently replace, and this file is the one output rasqlgen
// must never replace once it exists, so it is written directly rather than
// through genfile.Write. schemaOutputFilename's own "<table>_gen.go"
// naming can never collide with it.
const descriptionHintsFilename = "hints.go"

// WriteDescriptionPackage writes a bootstrap-generated description package
// into directory: one file per table, named the same way WritePackage names
// its own per-table files (schemaOutputFilename), each exporting a function
// that returns that table's schema.TableDef, plus
// descriptionAggregatorFilename exporting func Tables() []schema.TableDef in
// Name order. The result is immediately valid `rasqlgen schema -source`
// input.
//
// Unlike WritePackage, which regenerates a package it already wrote,
// WriteDescriptionPackage refuses whenever directory already holds any
// entries at all, hand-written or bootstrap's own earlier output alike:
// refreshing an existing description package is not supported yet, and is
// left for a follow-up.
func WriteDescriptionPackage(packageName, directory string, tables ...schema.TableDef) error {
	if len(tables) == 0 {
		return fmt.Errorf("bootstrap output %q: no tables to describe", directory)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("bootstrap output %q is not a directory", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("bootstrap output %q is not empty; refreshing an existing description package is not supported yet, write into an empty directory", directory)
	}

	sorted := append([]schema.TableDef(nil), tables...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left].Name < sorted[right].Name
	})

	files := make([]descriptionFile, 0, len(sorted))
	filenames := make(map[string]string, len(sorted))
	funcNames := make(map[string]string, len(sorted))
	for _, table := range sorted {
		filename := schemaOutputFilename(table.Name)
		if filename == descriptionAggregatorFilename {
			return fmt.Errorf("bootstrap table %q generates %q, which collides with the aggregator file", table.Name, filename)
		}
		if other, exists := filenames[filename]; exists {
			return fmt.Errorf("bootstrap tables %q and %q both generate %q", other, table.Name, filename)
		}
		filenames[filename] = table.Name

		funcName := schemagen.DescriptionFuncName(table.Name)
		if other, exists := funcNames[funcName]; exists {
			return fmt.Errorf("bootstrap tables %q and %q both generate function %s", other, table.Name, funcName)
		}
		funcNames[funcName] = table.Name

		files = append(files, descriptionFile{table: table, filename: filename, funcName: funcName})
	}

	for _, file := range files {
		source, err := schemagen.DescriptionSource(packageName, file.funcName, file.table)
		if err != nil {
			return err
		}
		if err := genfile.Write(filepath.Join(directory, file.filename), source); err != nil {
			return fmt.Errorf("write table %q: %w", file.table.Name, err)
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
	if err := genfile.Write(filepath.Join(directory, descriptionAggregatorFilename), aggregatorSource); err != nil {
		return fmt.Errorf("write %s: %w", descriptionAggregatorFilename, err)
	}
	if err := writeHintsFileIfAbsent(packageName, directory); err != nil {
		return fmt.Errorf("write %s: %w", descriptionHintsFilename, err)
	}
	return nil
}

// writeHintsFileIfAbsent writes descriptionHintsFilename into directory the
// first time it does not already exist there, and does nothing otherwise --
// the file is user-owned from the moment it is written, so a caller that
// runs this again after editing it, whether WriteDescriptionPackage on a
// package that predates hints or a later refresh, must never touch it
// again. os.Stat and os.WriteFile are not one atomic step, but the race
// this leaves -- two rasqlgen invocations bootstrapping the same output
// directory at once -- is already unsupported by WriteDescriptionPackage's
// own directory-emptiness check above, which is subject to the identical
// race.
func writeHintsFileIfAbsent(packageName, directory string) error {
	path := filepath.Join(directory, descriptionHintsFilename)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	source, err := schemagen.HintsSource(packageName)
	if err != nil {
		return err
	}
	return os.WriteFile(path, source, 0o600)
}

// descriptionFile is one table's resolved destination and function name,
// computed once so WriteDescriptionPackage checks every collision before it
// writes anything.
type descriptionFile struct {
	table    schema.TableDef
	filename string
	funcName string
}
