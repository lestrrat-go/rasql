package generate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/genfile"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

// QueryPackage describes a generated package made only from static SQL
// queries. It does not inspect a database and does not require any table
// descriptors. Queries are rendered in output-name order, so their order in
// the input slice does not affect the generated package.
//
// QueryPackage follows the same lifecycle as Store: Plan validates and
// renders without writing, Write plans and commits, and Check reports whether
// the files on disk are current.
type QueryPackage struct {
	// Package is the generated package name. It is required and must be a
	// valid Go package identifier other than "_".
	Package string

	// Dir is the directory containing the generated query files. It is
	// resolved against Root and is created by Write when it does not exist.
	Dir string

	// Root is the base directory for relative Dir and Query.Input paths.
	// Empty means the module root found from the process working directory.
	Root string

	// Dialect is used by queries whose Dialect is nil. It is required when
	// any such query is present.
	Dialect dialect.Dialect

	// Queries are the static SQL templates to compile into the package.
	// At least one query is required.
	Queries []Query
}

// QueryPlan is a rendered, uncommitted query package. Its files contain the
// complete generated sources and are sorted by destination path. Orphans
// records marked generated files in Dir that this plan does not write.
type QueryPlan struct {
	files   []File
	orphans []string
	dir     string
	root    string
}

// Files reports the files the plan writes. The result and every source byte
// slice are copies, so callers cannot mutate the plan.
func (p QueryPlan) Files() []File {
	files := make([]File, len(p.files))
	for i, file := range p.files {
		files[i] = File{
			Path:     file.Path,
			Resolved: file.Resolved,
			Source:   append([]byte(nil), file.Source...),
		}
	}
	return files
}

// Plan validates and renders the query package without writing anything.
// Every query input is read and compiled, every output is checked for a safe
// generated-file name, every existing Go file must belong to the package, and
// every existing generated destination must carry the rasqlgen marker. Marked
// generated files that are not planned outputs are recorded as orphans. No
// table schema is consulted.
func (p QueryPackage) Plan() (QueryPlan, error) {
	if p.Package == "" {
		return QueryPlan{}, errors.New("generate: query package requires Package")
	}
	if err := Validate(p.Package); err != nil {
		return QueryPlan{}, err
	}
	if p.Dir == "" {
		return QueryPlan{}, errors.New("generate: query package requires Dir")
	}
	if len(p.Queries) == 0 {
		return QueryPlan{}, errors.New("generate: query package requires at least one query")
	}

	root, err := resolveModuleRoot(p.Root)
	if err != nil {
		return QueryPlan{}, err
	}
	dir, err := resolveQueryPackagePath(root, p.Dir, "Dir")
	if err != nil {
		return QueryPlan{}, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return QueryPlan{}, fmt.Errorf("generate: resolve Dir: %w", err)
	}
	checkRoot := root
	if checkRoot != "" {
		checkRoot, err = filepath.Abs(checkRoot)
		if err != nil {
			return QueryPlan{}, fmt.Errorf("generate: resolve Root: %w", err)
		}
	}

	queries := append([]Query(nil), p.Queries...)
	sort.SliceStable(queries, func(left, right int) bool {
		if queries[left].Output != queries[right].Output {
			return queries[left].Output < queries[right].Output
		}
		return queries[left].Function < queries[right].Function
	})

	filenames := make(map[string]string, len(queries))
	functions := make(map[string]string, len(queries))
	files := make([]File, 0, len(queries))
	for _, query := range queries {
		file, err := p.planQuery(root, dir, query, filenames, functions)
		if err != nil {
			return QueryPlan{}, err
		}
		files = append(files, file)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	if err := requireQueryPackageOwnsDir(dir, p.Package, files); err != nil {
		return QueryPlan{}, err
	}
	orphans, _, err := findOrphans(dir, files)
	if err != nil {
		return QueryPlan{}, fmt.Errorf("generate: scan %s for leftover files: %w", dir, err)
	}
	return QueryPlan{files: files, orphans: orphans, dir: dir, root: checkRoot}, nil
}

// Write plans and commits the query package. The output directory and its
// missing parents are created as needed. Each file is published through
// genfile's atomic replacement path, and all destinations are authorized
// before the first file is replaced.
func (p QueryPackage) Write() error {
	plan, err := p.Plan()
	if err != nil {
		return err
	}
	return plan.Commit()
}

// Check reports whether the generated query package is current without
// writing anything. It returns an error wrapping ErrStale when a file is
// missing or differs from the current query inputs.
func (p QueryPackage) Check() error {
	plan, err := p.Plan()
	if err != nil {
		return err
	}
	return plan.Check()
}

// Commit publishes every file in the plan. It refuses recorded orphans and
// validates every destination before writing, so a stale generated file,
// marker refusal, or another predictable destination error cannot leave an
// earlier query file replaced.
func (p QueryPlan) Commit() error {
	if p.dir == "" {
		return errors.New("generate: zero QueryPlan cannot be committed; only QueryPackage.Plan builds a plan")
	}
	if len(p.orphans) > 0 {
		return fmt.Errorf("generate: %s holds %d file(s) rasqlgen wrote that this plan does not write: %s; remove them before writing the query package", p.dir, len(p.orphans), strings.Join(p.orphans, ", "))
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return fmt.Errorf("generate: create query output directory %q: %w", p.dir, err)
	}

	destinations := make([]string, len(p.files))
	seen := make(map[string]string, len(p.files))
	for index, file := range p.files {
		destination, err := genfile.ResolveDestination(file.Path)
		if err != nil {
			return fmt.Errorf("generate: authorize query output %s: %w", file.Path, err)
		}
		key := filepath.Clean(destination)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("generate: query outputs %s and %s resolve to the same destination %s", previous, file.Path, destination)
		}
		seen[key] = file.Path
		destinations[index] = destination
	}

	for index, file := range p.files {
		if err := genfile.Write(destinations[index], file.Source); err != nil {
			return fmt.Errorf("generate: write query output %s: %w", file.Path, err)
		}
	}
	return nil
}

// Check compares every planned file with its destination without writing.
// Missing, differing, and orphaned generated files are reported together and
// wrap ErrStale.
func (p QueryPlan) Check() error {
	if p.dir == "" {
		return errors.New("generate: zero QueryPlan cannot be checked; only QueryPackage.Plan builds a plan")
	}
	var stale []string
	for _, file := range p.files {
		data, err := os.ReadFile(file.Resolved)
		if errors.Is(err, os.ErrNotExist) {
			stale = append(stale, fmt.Sprintf("%s: is missing", formatCheckPath(p.root, file.Path)))
			continue
		}
		if err != nil {
			return fmt.Errorf("generate: read %s: %w", file.Resolved, err)
		}
		if !bytes.Equal(data, file.Source) {
			stale = append(stale, fmt.Sprintf("%s: differs", formatCheckPath(p.root, file.Path)))
		}
	}
	for _, orphan := range p.orphans {
		stale = append(stale, fmt.Sprintf("%s: is an orphaned generated file", formatCheckPath(p.root, orphan)))
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return fmt.Errorf("%w: %s", ErrStale, strings.Join(stale, "; "))
}

func requireQueryPackageOwnsDir(dir, pkg string, planned []File) error {
	own := make([]string, len(planned))
	for i, file := range planned {
		own[i] = filepath.Base(file.Path)
	}
	name, declared, err := DirForeignPackage(dir, pkg, own...)
	if err != nil {
		return fmt.Errorf("generate: scan %s for package ownership: %w", dir, err)
	}
	if name == "" {
		return nil
	}
	return fmt.Errorf("generate: %s already holds package %q, declared by %s, and this query package generates package %q; a directory holds one package, so writing the query package there would leave a directory no build can compile", dir, declared, name, pkg)
}

func (p QueryPackage) planQuery(root, dir string, query Query, filenames, functions map[string]string) (File, error) {
	if query.Input == "" {
		return File{}, errors.New("generate: query input is required")
	}
	if query.Function == "" {
		return File{}, errors.New("generate: query function is required")
	}
	if query.Output == "" {
		return File{}, errors.New("generate: query output is required")
	}
	if query.Output != filepath.Base(query.Output) {
		return File{}, fmt.Errorf("generate: query output %q must be a file name directly inside the query package Dir, not a path", query.Output)
	}
	if !strings.HasSuffix(query.Output, "_gen.go") {
		return File{}, fmt.Errorf("generate: query output %q must end in _gen.go", query.Output)
	}
	if owner, exists := filenames[filenameKey(query.Output)]; exists {
		return File{}, fmt.Errorf("generate: query %q output %q collides with %s", query.Function, query.Output, owner)
	}
	if owner, exists := functions[query.Function]; exists {
		return File{}, fmt.Errorf("generate: query function %q collides with %s", query.Function, owner)
	}

	d := query.Dialect
	if d == nil {
		d = p.Dialect
	}
	inputPath, err := resolveQueryPackagePath(root, query.Input, "Query.Input")
	if err != nil {
		return File{}, err
	}
	data, err := readQueryInput(inputPath)
	if err != nil {
		return File{}, fmt.Errorf("generate: read query input %s: %w", inputPath, err)
	}
	parsed, err := querytemplate.Parse(query.Function, string(data))
	if err != nil {
		return File{}, fmt.Errorf("generate: validate query %q: %w", query.Function, err)
	}
	compiled, err := parsed.Compile(d)
	if err != nil {
		return File{}, fmt.Errorf("generate: compile query %q: %w", query.Function, err)
	}
	source, err := compiled.GoSource(p.Package, query.Function)
	if err != nil {
		return File{}, fmt.Errorf("generate: render query %q: %w", query.Function, err)
	}

	filenames[filenameKey(query.Output)] = fmt.Sprintf("query %q", query.Function)
	functions[query.Function] = fmt.Sprintf("query output %q", query.Output)
	path := filepath.Join(dir, query.Output)
	destination, err := genfile.ResolveDestination(path)
	if err != nil {
		return File{}, fmt.Errorf("generate: authorize query output %s: %w", path, err)
	}
	return File{Path: path, Resolved: destination, Source: source}, nil
}

func resolveQueryPackagePath(root, path, field string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if root == "" {
		return "", fmt.Errorf("generate: relative %s %q cannot be resolved: QueryPackage.Root is empty and no go.mod was found above the working directory", field, path)
	}
	return filepath.Join(root, path), nil
}
