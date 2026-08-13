package generate

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
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
//
// The per-table files hold the generated surface without the descriptors,
// which schemaDescriptorFilename declares once for the whole package, so a
// run must name every table the package already holds a file for; see
// requireNoOrphanedTableFiles.
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
	// Every destination is resolved before the first one is written. A
	// schema run writes a package, not a file, and a refusal discovered
	// while writing the third file would leave the first two already
	// replaced and the rest of the package as it was.
	for _, destination := range schemaOutputPaths(directory, sorted) {
		if _, err := genfile.ResolveDestination(destination); err != nil {
			return err
		}
	}
	if err := requireNoOrphanedTableFiles(directory, sorted); err != nil {
		return err
	}
	for _, table := range sorted {
		source, err := schemagen.TableSurfaceSource(packageName, table, sorted...)
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

// requireNoOrphanedTableFiles refuses a run that would leave a generated
// file behind without the table value it reads.
//
// A schema run rewrites schema_gen.go from the tables it was given, so a
// table the run leaves out loses its descriptor there. The file generated
// for that table earlier is not rewritten and not examined by the
// overwrite guard, which looks only at the destinations this run writes, so
// it stays behind reading a value nothing declares any more and the package
// stops compiling on the next build.
//
// The refusal happens before the first file is written, so a run that would
// end this way leaves the directory exactly as it was. Deleting the file
// instead is not this package's decision to make: the file is the user's,
// its name is one a person could have chosen, and a run that silently
// removed it would be indistinguishable from one that regenerated it.
func requireNoOrphanedTableFiles(directory string, tables []schema.TableDef) error {
	previous, err := descriptorFileTableNames(filepath.Join(directory, schemaDescriptorFilename))
	if err != nil {
		return err
	}
	generated := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		generated[table.Name] = struct{}{}
	}
	orphans := make([]string, 0, len(previous))
	for _, name := range previous {
		if _, exists := generated[name]; exists {
			continue
		}
		filename := schemaOutputFilename(name)
		if _, err := os.Lstat(filepath.Join(directory, filename)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		orphans = append(orphans, fmt.Sprintf("%s was generated for table %q, which this run leaves out, and would no longer find the %s value it reads", filename, name, schemagen.DescriptorVarName(name)))
	}
	if len(orphans) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to write schema output into %s: %s; name every table the package needs in one run, or delete the file first", directory, strings.Join(orphans, "; "))
}

// descriptorFileTableNames reports the tables the descriptor file at path
// declares, and reports none when that file does not exist yet.
//
// The names come from the Name field of every schema.TableDef literal the
// file assigns to a package-level variable, which is what DescriptorSource
// writes there, one per table. Reading the file rather than the directory
// listing is what makes the answer exact: it states which tables the
// package was generated for, where a file name only suggests it, and a
// query file generated beside it declares no such literal and so
// contributes nothing.
func descriptorFileTableNames(path string) ([]string, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	// A descriptor file that no longer parses has been edited since
	// rasqlgen wrote it, marker and all, so nothing here can say which
	// tables the package holds. Saying so beats guessing: proceeding could
	// strand a file this check exists to protect.
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("cannot read the tables %s was generated for: %w; delete it and rerun to regenerate the package", path, err)
	}
	names := make([]string, 0)
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, expression := range value.Values {
				name, ok := tableDefLiteralName(expression)
				if ok {
					names = append(names, name)
				}
			}
		}
	}
	return names, nil
}

// tableDefLiteralName reports the table a schema.TableDef composite literal
// names. Anything else, including a literal of another type, reports false.
func tableDefLiteralName(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return "", false
	}
	selector, ok := literal.Type.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "TableDef" {
		return "", false
	}
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		text, ok := field.Value.(*ast.BasicLit)
		if !ok || text.Kind != token.STRING {
			continue
		}
		name, err := strconv.Unquote(text.Value)
		if err != nil {
			return "", false
		}
		return name, true
	}
	return "", false
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

// schemaOutputPaths reports every path a WritePackage call into directory
// writes, in the order it writes them: one file per table, then the two it
// writes once per package. tables must already be sorted, and its filenames
// must already have passed the collision check, so that the paths reported
// here are the ones actually written.
func schemaOutputPaths(directory string, tables []schema.TableDef) []string {
	paths := make([]string, 0, len(tables)+2)
	for _, table := range tables {
		paths = append(paths, filepath.Join(directory, schemaOutputFilename(table.Name)))
	}
	return append(paths,
		filepath.Join(directory, schemaDescriptorFilename),
		filepath.Join(directory, schemaDescriptorTestFilename),
	)
}
