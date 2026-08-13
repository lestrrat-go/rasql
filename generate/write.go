package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
)

// WritePackage writes one generated file per table into directory, named
// <table>_gen.go in lowercase, plus the two files it writes once per
// package: schemaDescriptorFilename, holding every table's runtime
// descriptor, and schemaDescriptorTestFilename, the generated test that
// validates them. It validates packageName and tables before writing
// anything, and it rejects two tables whose names would generate the same
// filename before either file is written. Each file is written through
// genfile.Write, which never truncates an existing file in place.
func WritePackage(packageName, directory string, tables ...schema.TableDef) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("schema output %q is not a directory", directory)
	}
	if err := Validate(packageName, tables...); err != nil {
		return err
	}

	sorted := append([]schema.TableDef(nil), tables...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left].Name < sorted[right].Name
	})
	filenames := make(map[string]string, len(sorted))
	for _, table := range sorted {
		filename := schemaOutputFilename(table.Name)
		if filename == schemaDescriptorFilename {
			return fmt.Errorf("schema table %q generates %q, which collides with the descriptor file", table.Name, filename)
		}
		if other, exists := filenames[filename]; exists {
			return fmt.Errorf("schema tables %q and %q both generate %q", other, table.Name, filename)
		}
		filenames[filename] = table.Name
	}
	for _, table := range sorted {
		source, err := TableSource(packageName, table, sorted...)
		if err != nil {
			return err
		}
		filename := schemaOutputFilename(table.Name)
		if err := genfile.Write(filepath.Join(directory, filename), source); err != nil {
			return fmt.Errorf("write table %q: %w", table.Name, err)
		}
	}

	descriptorSource, err := DescriptorSource(packageName, sorted...)
	if err != nil {
		return err
	}
	if err := genfile.Write(filepath.Join(directory, schemaDescriptorFilename), descriptorSource); err != nil {
		return fmt.Errorf("write %s: %w", schemaDescriptorFilename, err)
	}

	descriptorTestSource, err := DescriptorTestSource(packageName, sorted...)
	if err != nil {
		return err
	}
	if err := genfile.Write(filepath.Join(directory, schemaDescriptorTestFilename), descriptorTestSource); err != nil {
		return fmt.Errorf("write %s: %w", schemaDescriptorTestFilename, err)
	}
	return nil
}

// schemaDescriptorFilename and schemaDescriptorTestFilename are the two
// files WritePackage writes once per package rather than once per table. A
// table literally named "schema" would otherwise generate
// schemaDescriptorFilename itself through schemaOutputFilename, which the
// collision check above rejects before anything is written.
const (
	schemaDescriptorFilename     = "schema_gen.go"
	schemaDescriptorTestFilename = "schema_gen_test.go"
)

func schemaOutputFilename(tableName string) string {
	return strings.ToLower(tableName) + "_gen.go"
}
