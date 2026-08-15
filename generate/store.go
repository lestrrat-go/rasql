package generate

import (
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/template"
)

// Store describes one generated store package: which tables it is
// generated from, where it goes, and what else belongs in the same
// directory.
//
// A Store is a value and holds no state of its own. It touches neither the
// filesystem nor a database until one of its methods runs, and none of its
// methods mutates it, so two field-wise equal Stores plan byte-identical
// output.
type Store struct {
	// Package is the generated package name. Required, and must be a Go
	// identifier that is not the blank identifier: "package _" is not a
	// package clause the compiler accepts.
	Package string

	// Dir is the directory the package is written into, resolved against
	// Root when relative. Required. A later Write creates it, and every
	// parent, when it does not exist.
	Dir string

	// Root is the directory relative paths in Dir and in each Query
	// resolve against. Empty means the module root: the directory
	// holding the nearest go.mod at or above the process working
	// directory. That default is what makes Dir mean the same thing
	// wherever the //go:generate line that runs this program happens to
	// live. When Root is empty and no go.mod is found, a relative path
	// is an error rather than a guess.
	Root string

	// Tables are the descriptors to generate from. Required and
	// non-empty. Each is validated, cloned, and sorted by Name before
	// rendering, so the caller's slice is never modified and its order
	// does not reach the output.
	Tables []schema.TableDef

	// Hints overlay Go-side facts no database can state, keyed by
	// schema.TableDef.Name. A key naming no table in Tables is an error,
	// and so is a key matching more than one table, which two same-named
	// tables in different schemas can produce; set the fact on the
	// descriptor itself in that case.
	//
	// A hint is an overlay, so it can set a fact but not clear one, and a
	// fact it sets is rendered into the generated descriptor and
	// therefore comes back out of the store's own Tables(). Deleting a
	// hint from this map does not un-set it for a run generated from
	// that store; it does for a run generated from a database.
	Hints map[string]schema.TableHint

	// Dialect selects the placeholder style for a Query that does not
	// name its own. Required when any Query leaves Dialect nil, ignored
	// otherwise. It is not used to render the tables, which are
	// dialect-independent.
	Dialect dialect.Dialect

	// Queries are static SQL templates compiled into this package, one
	// generated function each.
	Queries []Query

	// Prune allows a run to delete a file in Dir that rasqlgen wrote and
	// this plan does not write -- the per-table file of a table that was
	// dropped, say. False refuses the run instead, naming every such
	// file. See Plan.Orphans.
	Prune bool
}

// Query is one static SQL template compiled into a generated function
// inside the store package.
type Query struct {
	// Input is the template file, resolved against the Store's Root when
	// relative. Required.
	Input string

	// Function is the generated function name. Required, and must be an
	// exported Go identifier that no other declaration in the package
	// already takes: not one the generated tables, descriptors or
	// descriptor test declare, and not another Query's Function.
	Function string

	// Output is the file the function is generated into, a file name
	// rather than a path: it names a file directly inside the Store's
	// Dir, and must end in _gen.go. A value carrying a directory, a ".."
	// element or an absolute path is an error rather than a destination,
	// so the planned File.Path is always directly inside Dir rather than
	// outside it or in a subdirectory of it. Where that path then
	// resolves is a separate question: a symbolic link in Dir is followed
	// rather than refused, and File.Resolved reports what a commit would
	// actually replace. What such a link may not do is land this file on
	// a destination another planned file also resolves to, which Plan
	// rejects. Required.
	Output string

	// Dialect selects the placeholder style. Nil means the Store's own
	// Dialect.
	Dialect dialect.Dialect
}

// Plan renders every file this store needs and reports what a run would do,
// without writing anything or deleting anything.
//
// It resolves Root and Dir, validates the package name, every descriptor,
// every hint key, and every generated identifier; renders every per-table
// file, the descriptor file, the generated descriptor test, and every
// query; rejects two files that would land on the same path, a query output
// that is not a plain file name inside Dir, and a query function that
// redeclares a name the generated package already uses; refuses a
// destination that exists and is not a file rasqlgen wrote; and records
// every file already in Dir that rasqlgen wrote and this plan does not
// write.
//
// Each planned file also carries the destination its path resolves to, in
// File.Resolved: a path that is a symbolic link, or that sits in a
// directory reached through one, resolves through it exactly as a commit's
// own write would, so a file whose bytes land outside Dir says so instead
// of being described by File.Path alone. Resolving through a link is
// allowed, and where it resolves to is not restricted; two planned files
// that resolve to one destination are an error, since a commit would write
// both and keep only the last.
func (s Store) Plan() (Plan, error) {
	// The blank identifier is checked separately because
	// token.IsIdentifier accepts it: it is an identifier everywhere else
	// in Go, but "package _" is not a package clause the compiler
	// accepts, so a plan built from it renders files that cannot build.
	// Nothing narrower is refused here. A keyword such as "func" is
	// already refused by token.IsIdentifier, and "package init" is a
	// legal package clause, so it stays accepted.
	if s.Package == "_" {
		return Plan{}, errors.New("generate: store package cannot be the blank identifier")
	}
	if !token.IsIdentifier(s.Package) {
		return Plan{}, fmt.Errorf("generate: store package %q must be a Go identifier", s.Package)
	}
	if s.Dir == "" {
		return Plan{}, errors.New("generate: store requires Dir")
	}
	if len(s.Tables) == 0 {
		return Plan{}, errors.New("generate: store requires at least one table")
	}

	root, err := resolveModuleRoot(s.Root)
	if err != nil {
		return Plan{}, err
	}
	dir, err := resolveStorePath(root, s.Dir)
	if err != nil {
		return Plan{}, fmt.Errorf("generate: resolve Dir: %w", err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return Plan{}, fmt.Errorf("generate: resolve Dir: %w", err)
	}

	tables, err := applyHints(s.Tables, s.Hints)
	if err != nil {
		return Plan{}, err
	}
	sorted := append([]schema.TableDef(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	if err := Validate(s.Package, sorted...); err != nil {
		return Plan{}, err
	}

	// filenames tracks every destination file name (not path) that a
	// table or the descriptor file already claims, so a query can be
	// checked against the same set below. Two tables that lower to the
	// same file name collide even when schemagen assigns them distinct
	// generated identifiers, which is why this check exists beside
	// Validate rather than instead of it.
	filenames := make(map[string]string, len(sorted)+1+len(s.Queries))
	filenames[schemaDescriptorFilename] = "the schema descriptor file"
	for _, table := range sorted {
		filename := schemaOutputFilename(table.Name)
		if owner, exists := filenames[filename]; exists {
			return Plan{}, fmt.Errorf("generate: table %q generates %q, which collides with %s", table.Name, filename, owner)
		}
		filenames[filename] = fmt.Sprintf("table %q", table.Name)
	}

	// identifiers tracks every package-level identifier the generated files
	// already declare, so a query whose Function would redeclare one is
	// refused here instead of producing a plan that does not compile. A
	// query is the only declaration a Store adds that the descriptors do not
	// explain, which is why this set is built once here and handed to
	// planQuery rather than derived per query.
	declared, err := schemagen.PackageLevelNames(s.Package, sorted...)
	if err != nil {
		return Plan{}, err
	}
	identifiers := make(map[string]string, len(declared)+len(s.Queries))
	for _, name := range declared {
		identifiers[name] = "an identifier the generated store already declares"
	}

	files := make([]File, 0, len(sorted)+2+len(s.Queries))
	for _, table := range sorted {
		source, err := schemagen.TableSurfaceSource(s.Package, table, sorted...)
		if err != nil {
			return Plan{}, err
		}
		files = append(files, File{Path: filepath.Join(dir, schemaOutputFilename(table.Name)), Source: source})
	}

	descriptorSource, err := DescriptorSource(s.Package, sorted...)
	if err != nil {
		return Plan{}, err
	}
	files = append(files, File{Path: filepath.Join(dir, schemaDescriptorFilename), Source: descriptorSource})

	descriptorTestSource, err := DescriptorTestSource(s.Package, sorted...)
	if err != nil {
		return Plan{}, err
	}
	files = append(files, File{Path: filepath.Join(dir, schemaDescriptorTestFilename), Source: descriptorTestSource})

	for index, q := range s.Queries {
		file, err := s.planQuery(root, dir, q, filenames, identifiers)
		if err != nil {
			return Plan{}, fmt.Errorf("generate: query[%d]: %w", index, err)
		}
		files = append(files, file)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	// The resolved destination is kept rather than discarded: it is the
	// file a commit's bytes actually land in, and it differs from the
	// planned path whenever that path, or a directory holding it, is a
	// symbolic link. Discarding it would leave File.Path claiming a
	// destination nothing verified it had.
	//
	// destinations holds every resolved destination so two planned files
	// that land in one file are refused. The name check above cannot see
	// this: two distinct names inside Dir are distinct keys there, yet a
	// symbolic link can point both at one file, and a commit would then
	// write both and keep only the last. This is the only place that
	// holds every resolved destination at once. Resolving somewhere
	// outside Dir stays allowed -- genfile.Write follows an output link
	// on purpose -- and only a destination a second planned file also
	// claims is an error.
	destinations := make(map[string]string, len(files))
	for i := range files {
		resolved, err := genfile.ResolveDestination(files[i].Path)
		if err != nil {
			return Plan{}, err
		}
		if owner, exists := destinations[resolved]; exists {
			return Plan{}, fmt.Errorf("generate: %s and %s both resolve to %s, so one would overwrite the other", owner, files[i].Path, resolved)
		}
		destinations[resolved] = files[i].Path
		files[i].Resolved = resolved
	}

	orphans, err := findOrphans(dir, files)
	if err != nil {
		return Plan{}, fmt.Errorf("generate: scan %s for leftover files: %w", dir, err)
	}

	return Plan{files: files, orphans: orphans}, nil
}

// planQuery renders one Query into its File, checking its output name
// against filenames -- the file names every table and the descriptor file
// already claim -- and its function name against identifiers -- the
// package-level names the generated files already declare -- and recording
// both once they pass, so a later query is checked against them too.
func (s Store) planQuery(root, dir string, q Query, filenames, identifiers map[string]string) (File, error) {
	if q.Input == "" {
		return File{}, errors.New("input is required")
	}
	if q.Function == "" {
		return File{}, errors.New("function is required")
	}
	if !isExportedGoIdentifier(q.Function) {
		return File{}, fmt.Errorf("function %q must be an exported Go identifier", q.Function)
	}
	if owner, exists := identifiers[q.Function]; exists {
		return File{}, fmt.Errorf("function %q collides with %s", q.Function, owner)
	}
	if q.Output == "" {
		return File{}, errors.New("output is required")
	}
	if err := validateQueryOutputName(q.Output); err != nil {
		return File{}, err
	}
	if !strings.HasSuffix(q.Output, "_gen.go") {
		return File{}, fmt.Errorf("output %q must end in _gen.go", q.Output)
	}
	if owner, exists := filenames[q.Output]; exists {
		return File{}, fmt.Errorf("query %q output %q collides with %s", q.Function, q.Output, owner)
	}
	filenames[q.Output] = fmt.Sprintf("query %q", q.Function)
	identifiers[q.Function] = fmt.Sprintf("query %q", q.Function)

	d := q.Dialect
	if d == nil {
		d = s.Dialect
	}

	inputPath, err := resolveStorePath(root, q.Input)
	if err != nil {
		return File{}, fmt.Errorf("resolve Input: %w", err)
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return File{}, fmt.Errorf("read Input %s: %w", inputPath, err)
	}
	parsed, err := template.Parse(q.Function, string(data))
	if err != nil {
		return File{}, err
	}
	compiled, err := parsed.Compile(d)
	if err != nil {
		return File{}, err
	}
	source, err := compiled.GoSource(s.Package, q.Function)
	if err != nil {
		return File{}, err
	}
	return File{Path: filepath.Join(dir, q.Output), Source: source}, nil
}

// validateQueryOutputName checks that output is the plain file name
// Query.Output documents itself to be, rather than any kind of path.
//
// The check is load-bearing twice over. The collision check above is keyed on
// the name while the destination is built with filepath.Join, which cleans
// it, so an unvalidated "nested/../users_gen.go" matches no key the users
// table registered yet lands on that table's own path: two planned files, one
// destination. And filepath.Join keeps whatever a cleaned "../" or an
// absolute path resolves to, so the same gap lets a planned File.Path leave
// Dir entirely. Requiring the name to be its own filepath.Base closes both,
// and rejects "sub/x_gen.go" with them, since Plan creates no directory and a
// later run writing into one would have to.
func validateQueryOutputName(output string) error {
	if output != filepath.Base(output) {
		return fmt.Errorf("output %q must be a file name directly inside the store's Dir, not a path", output)
	}
	return nil
}

// isExportedGoIdentifier reports whether name is a valid, exported Go
// identifier: an ordinary Go identifier whose first rune is uppercase.
func isExportedGoIdentifier(name string) bool {
	if !token.IsIdentifier(name) {
		return false
	}
	first, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(first)
}

// applyHints returns a clone of tables with every hint in hints overlaid,
// leaving tables itself untouched. A hint key that names no table, or that
// names more than one -- which two same-named tables in different schemas
// can produce -- is an error.
func applyHints(tables []schema.TableDef, hints map[string]schema.TableHint) ([]schema.TableDef, error) {
	clones := make([]schema.TableDef, len(tables))
	for i, table := range tables {
		clones[i] = table.Clone()
	}
	if len(hints) == 0 {
		return clones, nil
	}

	keys := make([]string, 0, len(hints))
	for key := range hints {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		matches := make([]int, 0, 1)
		for i, table := range clones {
			if table.Name == key {
				matches = append(matches, i)
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("generate: hint key %q names no table in Tables", key)
		case 1:
			clones[matches[0]] = hints[key].Apply(clones[matches[0]])
		default:
			return nil, fmt.Errorf("generate: hint key %q matches more than one table in Tables; set the fact on the descriptor itself instead", key)
		}
	}
	return clones, nil
}

// resolveModuleRoot returns explicit unchanged when it is non-empty, and
// otherwise walks upward from the process working directory looking for a
// directory holding go.mod. It reports an empty string, not an error, when
// none is found: whether that matters depends on whether a relative path
// ever needs it, which resolveStorePath decides.
func resolveModuleRoot(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("generate: determine working directory: %w", err)
	}
	dir := wd
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// resolveStorePath resolves path against root when path is relative. An
// absolute path is returned unchanged. A relative path with an empty root
// -- Root left empty with no go.mod found above the working directory -- is
// an error naming Store.Root, since there is nothing to resolve it against.
func resolveStorePath(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if root == "" {
		return "", fmt.Errorf("relative path %q cannot be resolved: Store.Root is empty and no go.mod was found above the working directory", path)
	}
	return filepath.Join(root, path), nil
}
