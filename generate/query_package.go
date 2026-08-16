package generate

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
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
	files       []File
	orphans     []string
	inputs      []queryInputSnapshot
	packageName string
	functions   []string
	dir         string
	root        string
	// anchor is the deepest directory on dir's own path that already
	// existed when QueryPackage.Plan looked, and anchorInfo is what the
	// filesystem reported for it. Commit reopens anchor, requires it to
	// still be the same directory, and walks down to dir without following
	// symbolic links that appeared after the plan was built.
	anchor     string
	anchorInfo fs.FileInfo
}

type queryInputSnapshot struct {
	path   string
	digest [sha256.Size]byte
}

type queryInputData struct {
	snapshot queryInputSnapshot
	data     []byte
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

// Orphans reports the marked generated files in Dir that this plan does not
// write, sorted by path. The result is a copy, so callers cannot mutate the
// plan.
func (p QueryPlan) Orphans() []string {
	return append([]string(nil), p.orphans...)
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
	if root != "" {
		root, err = filepath.Abs(root)
		if err != nil {
			return QueryPlan{}, fmt.Errorf("generate: resolve Root: %w", err)
		}
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
	inputs := make(map[string]queryInputData, len(queries))
	files := make([]File, 0, len(queries))
	for _, query := range queries {
		file, err := p.planQuery(root, dir, query, filenames, functions, inputs)
		if err != nil {
			return QueryPlan{}, err
		}
		files = append(files, file)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	queryFunctions := make([]string, len(queries))
	for i, query := range queries {
		queryFunctions[i] = query.Function
	}
	orphans, dirInfo, err := queryPackageOwnership(dir, p.Package, files, queryFunctions)
	if err != nil {
		return QueryPlan{}, err
	}
	anchor, anchorInfo := dir, dirInfo
	if anchorInfo == nil {
		anchor, anchorInfo, err = deepestExistingDirectory(dir)
		if err != nil {
			return QueryPlan{}, fmt.Errorf("generate: find an existing directory above %s: %w", dir, err)
		}
	}
	inputSnapshots := make([]queryInputSnapshot, 0, len(inputs))
	for _, input := range inputs {
		inputSnapshots = append(inputSnapshots, input.snapshot)
	}
	sort.Slice(inputSnapshots, func(left, right int) bool { return inputSnapshots[left].path < inputSnapshots[right].path })
	return QueryPlan{files: files, orphans: orphans, inputs: inputSnapshots, packageName: p.Package, functions: queryFunctions, dir: dir, root: checkRoot, anchor: anchor, anchorInfo: anchorInfo}, nil
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

// Commit publishes every file in the plan. It refuses recorded or newly
// appearing ownership conflicts and orphans, and validates every destination
// before writing, so a stale generated file, marker refusal, or another
// predictable destination error cannot leave an earlier query file replaced.
func (p QueryPlan) Commit() error {
	if p.dir == "" {
		return errors.New("generate: zero QueryPlan cannot be committed; only QueryPackage.Plan builds a plan")
	}
	if len(p.orphans) > 0 {
		return fmt.Errorf("generate: %s holds %d file(s) rasqlgen wrote that this plan does not write: %s; remove them before writing the query package", p.dir, len(p.orphans), strings.Join(p.orphans, ", "))
	}
	dir, err := openPlannedDirectory(p.anchor, p.anchorInfo, p.dir, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	realDir, err := filepath.EvalSymlinks(p.dir)
	if err != nil {
		return fmt.Errorf("generate: resolve %s: %w", p.dir, err)
	}
	dirInfo, err := dir.Stat(".")
	if err != nil {
		return fmt.Errorf("generate: check %s: %w", p.dir, err)
	}
	realDirInfo, err := os.Stat(realDir)
	if err != nil {
		return fmt.Errorf("generate: check %s: %w", realDir, err)
	}
	if !os.SameFile(dirInfo, realDirInfo) {
		return fmt.Errorf("generate: refusing to commit into %s: it is no longer the directory this commit authorized; rerun QueryPackage.Plan", p.dir)
	}

	handles := destinationDirectories{own: dir, ownPath: realDir}
	defer handles.close()
	writes := make([]commitWrite, len(p.files))
	seen := make(map[string]string, len(p.files))
	for index := range p.files {
		file := &p.files[index]
		destination, parentInfo, err := resolveCommitDestination(file.Path)
		if err != nil {
			return fmt.Errorf("generate: authorize query output %s: %w", file.Path, err)
		}
		if destination != file.Resolved {
			return fmt.Errorf("generate: refusing to commit query output %s: its destination changed after QueryPackage.Plan; rerun QueryPackage.Plan", file.Path)
		}
		key := filepath.Clean(destination)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("generate: query outputs %s and %s resolve to the same destination %s", previous, file.Path, destination)
		}
		seen[key] = file.Path
		into, err := handles.open(filepath.Dir(destination), parentInfo)
		if err != nil {
			return err
		}
		writes[index] = commitWrite{file: file, destination: destination, dir: into, name: filepath.Base(destination)}
	}
	orphans, _, err := queryPackageOwnershipAt(dir, p.dir, p.packageName, p.files, p.functions)
	if err != nil {
		return fmt.Errorf("%w; rerun QueryPackage.Plan", err)
	}
	if len(orphans) > 0 {
		return fmt.Errorf("generate: %s gained %d file(s) rasqlgen wrote that this plan does not write: %s; rerun QueryPackage.Plan", p.dir, len(orphans), strings.Join(orphans, ", "))
	}
	if err := p.validateQueryInputs(); err != nil {
		return err
	}

	for _, write := range writes {
		if err := genfile.WriteInto(write.dir, write.name, write.file.Source); err != nil {
			return fmt.Errorf("generate: write query output %s: %w", write.file.Path, err)
		}
	}
	return nil
}

// Check compares every planned file with its destination without writing.
// Missing, differing, and orphaned generated files are reported together and
// wrap ErrStale. A destination or directory that changed after planning is
// reported as a refusal because rerunning the plan is required before reading
// or writing through the changed path.
func (p QueryPlan) Check() error {
	if p.dir == "" {
		return errors.New("generate: zero QueryPlan cannot be checked; only QueryPackage.Plan builds a plan")
	}
	dir, err := openPlannedDirectory(p.anchor, p.anchorInfo, p.dir, false)
	if err != nil {
		return err
	}
	if dir != nil {
		defer func() { _ = dir.Close() }()
	}

	realDir := p.dir
	if dir != nil {
		realDir, err = filepath.EvalSymlinks(p.dir)
		if err != nil {
			return fmt.Errorf("generate: resolve %s: %w", p.dir, err)
		}
		dirInfo, err := dir.Stat(".")
		if err != nil {
			return fmt.Errorf("generate: check %s: %w", p.dir, err)
		}
		realDirInfo, err := os.Stat(realDir)
		if err != nil {
			return fmt.Errorf("generate: check %s: %w", realDir, err)
		}
		if !os.SameFile(dirInfo, realDirInfo) {
			return fmt.Errorf("generate: refusing to commit into %s: it is no longer the directory this commit authorized; rerun QueryPackage.Plan", p.dir)
		}
	}

	var handles destinationDirectories
	if dir != nil {
		handles = destinationDirectories{own: dir, ownPath: realDir}
	}
	defer handles.close()
	checks := make([]checkedFile, 0, len(p.files))
	seen := make(map[string]string, len(p.files))
	var stale []string
	for index := range p.files {
		file := &p.files[index]
		resolved, parentInfo, err := resolveCheckDestination(file.Path, p.dir, dir == nil)
		if err != nil {
			return err
		}
		if resolved != file.Resolved {
			return fmt.Errorf("generate: refusing to check query output %s: its destination changed after QueryPackage.Plan; rerun QueryPackage.Plan", file.Path)
		}
		key := filepath.Clean(resolved)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("generate: query outputs %s and %s resolve to the same destination %s", previous, file.Path, resolved)
		}
		seen[key] = file.Path
		var into *os.Root
		if dir != nil && parentInfo != nil {
			into, err = handles.open(filepath.Dir(resolved), parentInfo)
			if err != nil {
				return err
			}
		}
		checks = append(checks, checkedFile{file: file, destination: resolved, dir: into, name: filepath.Base(resolved)})
	}
	var orphans []string
	if dir == nil {
		orphans, _, err = queryPackageOwnership(p.dir, p.packageName, p.files, p.functions)
	} else {
		orphans, _, err = queryPackageOwnershipAt(dir, p.dir, p.packageName, p.files, p.functions)
	}
	if err != nil {
		return fmt.Errorf("%w; rerun QueryPackage.Plan", err)
	}
	for _, file := range checks {
		matches, err := file.matchesSource()
		switch {
		case errors.Is(err, fs.ErrNotExist):
			stale = append(stale, fmt.Sprintf("%s: is missing", formatCheckPath(p.root, file.file.Path)))
		case err != nil:
			return fmt.Errorf("generate: read %s: %w", file.destination, err)
		case !matches:
			stale = append(stale, fmt.Sprintf("%s: differs", formatCheckPath(p.root, file.file.Path)))
		}
	}
	for _, orphan := range orphans {
		stale = append(stale, fmt.Sprintf("%s: is an orphaned generated file", formatCheckPath(p.root, orphan)))
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return fmt.Errorf("%w: %s", ErrStale, strings.Join(stale, "; "))
}

func (p QueryPlan) validateQueryInputs() error {
	for _, input := range p.inputs {
		data, err := readQueryInput(input.path)
		if err != nil {
			return fmt.Errorf("generate: query input %s changed after QueryPackage.Plan: %w; rerun QueryPackage.Plan", formatCheckPath(p.root, input.path), err)
		}
		if sha256.Sum256(data) != input.digest {
			return fmt.Errorf("generate: query input %s changed after QueryPackage.Plan; rerun QueryPackage.Plan", formatCheckPath(p.root, input.path))
		}
	}
	return nil
}

func requireQueryPackageOwnsDir(root *os.Root, dir, pkg string, planned []File) error {
	own := make([]string, len(planned))
	for i, file := range planned {
		own[i] = filepath.Base(file.Path)
	}
	name, declared, err := queryForeignPackage(root, dir, pkg, own)
	if err != nil {
		return fmt.Errorf("generate: scan %s for package ownership: %w", dir, err)
	}
	if name == "" {
		return nil
	}
	return fmt.Errorf("generate: %s already holds package %q, declared by %s, and this query package generates package %q; a directory holds one package, so writing the query package there would leave a directory no build can compile", dir, declared, name, pkg)
}

func queryPackageOwnership(dir, pkg string, planned []File, functions []string) ([]string, fs.FileInfo, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	return queryPackageOwnershipAt(root, dir, pkg, planned, functions)
}

func queryPackageOwnershipAt(root *os.Root, dir, pkg string, planned []File, functions []string) ([]string, fs.FileInfo, error) {
	if err := requireQueryPackageOwnsDir(root, dir, pkg, planned); err != nil {
		return nil, nil, err
	}
	declared, err := queryPackageDeclaredNames(root, dir, pkg, planned)
	if err != nil {
		return nil, nil, fmt.Errorf("generate: scan %s for package declarations: %w", dir, err)
	}
	for _, function := range functions {
		if owner, exists := declared[function]; exists {
			return nil, nil, fmt.Errorf("generate: query function %q collides with %s", function, owner)
		}
	}
	orphans, dirInfo, err := findOrphansAt(root, dir, planned)
	if err != nil {
		return nil, nil, fmt.Errorf("generate: scan %s for leftover files: %w", dir, err)
	}
	return orphans, dirInfo, nil
}

// queryPackageDeclaredNames returns package-level declarations in active Go
// files that already belong to pkg. It also reads marked generated symlink
// targets because the toolchain compiles their resolved declarations while
// the symlink itself must be recorded as an orphan. Planned destinations are
// skipped because they are the files this plan will replace. A generated
// query package shares its package block with handwritten files, so a
// collision has to be rejected before Commit can publish any output.
func queryPackageDeclaredNames(root *os.Root, dir, pkg string, planned []File) (map[string]string, error) {
	own := make(map[string]struct{}, len(planned))
	for _, file := range planned {
		own[filepath.Base(file.Path)] = struct{}{}
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	context := activeBuildContext()
	declared := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if ownsEntry(root, own, name, info) {
			continue
		}
		resolved, err := queryPackageEntryStat(root, dir, name, info)
		if err != nil || !resolved.Mode().IsRegular() {
			continue
		}
		source, err := queryPackageEntrySource(root, dir, name, info)
		if err != nil {
			continue
		}
		included, err := queryPackageMatchFile(context, dir, name, source)
		if err != nil || !included {
			continue
		}
		marker, err := queryPackageEntryMarker(root, dir, name, info)
		if err != nil || (marker != nil && info.Mode()&fs.ModeSymlink == 0 && isGeneratedOutputName(name)) {
			continue
		}
		declaredPackage, err := queryPackageDeclaredPackage(name, source)
		if err != nil || declaredPackage != pkg {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv == nil {
					declared[declaration.Name.Name] = fmt.Sprintf("package-level declaration %q in %s", declaration.Name.Name, name)
				}
			case *ast.GenDecl:
				if declaration.Tok == token.IMPORT {
					continue
				}
				for _, specification := range declaration.Specs {
					switch specification := specification.(type) {
					case *ast.TypeSpec:
						declared[specification.Name.Name] = fmt.Sprintf("package-level declaration %q in %s", specification.Name.Name, name)
					case *ast.ValueSpec:
						for _, identifier := range specification.Names {
							declared[identifier.Name] = fmt.Sprintf("package-level declaration %q in %s", identifier.Name, name)
						}
					}
				}
			}
		}
	}
	return declared, nil
}

func queryForeignPackage(root *os.Root, dir, pkg string, own []string) (name string, declared string, err error) {
	ownSet := make(map[string]struct{}, len(own))
	for _, file := range own {
		ownSet[filepath.Base(file)] = struct{}{}
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return "", "", err
	}
	context := activeBuildContext()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", "", err
		}
		if ownsEntry(root, ownSet, name, info) {
			continue
		}
		resolved, err := queryPackageEntryStat(root, dir, name, info)
		if err != nil || !resolved.Mode().IsRegular() {
			continue
		}
		source, err := queryPackageEntrySource(root, dir, name, info)
		if err != nil {
			continue
		}
		included, err := queryPackageMatchFile(context, dir, name, source)
		if err != nil || !included {
			continue
		}
		marker, err := queryPackageEntryMarker(root, dir, name, info)
		if err != nil {
			continue
		}
		if marker != nil && isGeneratedOutputName(name) {
			continue
		}
		declaredName, err := queryPackageDeclaredPackage(name, source)
		if err != nil || declaredName == "" {
			continue
		}
		if declaredName == "documentation" || declaredName == pkg ||
			(declaredName == pkg+"_test" && strings.HasSuffix(name, "_test.go")) {
			continue
		}
		return name, declaredName, nil
	}
	return "", "", nil
}

func queryPackageEntryStat(root *os.Root, dir, name string, info fs.FileInfo) (fs.FileInfo, error) {
	resolved, err := root.Stat(name)
	if err == nil {
		return resolved, nil
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return nil, err
	}
	return os.Stat(filepath.Join(dir, name))
}

func queryPackageEntrySource(root *os.Root, dir, name string, info fs.FileInfo) ([]byte, error) {
	file, err := root.Open(name)
	if err == nil {
		defer func() { _ = file.Close() }()
		return io.ReadAll(file)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, name))
}

func queryPackageEntryMarker(root *os.Root, dir, name string, info fs.FileInfo) (fs.FileInfo, error) {
	if info.Mode()&fs.ModeSymlink != 0 {
		return readForeignMarker(filepath.Join(dir, name))
	}
	return readGenfileMarker(root, name)
}

func queryPackageMatchFile(context build.Context, dir, name string, source []byte) (bool, error) {
	context.OpenFile = func(path string) (io.ReadCloser, error) {
		if filepath.Base(path) == name {
			return io.NopCloser(bytes.NewReader(source)), nil
		}
		return os.Open(path)
	}
	return context.MatchFile(dir, name)
}

func queryPackageDeclaredPackage(name string, source []byte) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	return file.Name.Name, nil
}

func isGeneratedOutputName(name string) bool {
	return strings.HasSuffix(name, "_gen.go") || strings.HasSuffix(name, "_gen_test.go")
}

func (p QueryPackage) planQuery(root, dir string, query Query, filenames, functions map[string]string, inputs map[string]queryInputData) (File, error) {
	if query.Input == "" {
		return File{}, errors.New("generate: query input is required")
	}
	if query.Function == "" {
		return File{}, errors.New("generate: query function is required")
	}
	if !isExportedGoIdentifier(query.Function) {
		return File{}, fmt.Errorf("generate: query function %q must be an exported Go identifier", query.Function)
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
	input, exists := inputs[inputPath]
	if !exists {
		data, err := readQueryInput(inputPath)
		if err != nil {
			return File{}, fmt.Errorf("generate: read query input %s: %w", inputPath, err)
		}
		input = queryInputData{
			snapshot: queryInputSnapshot{path: inputPath, digest: sha256.Sum256(data)},
			data:     data,
		}
		inputs[inputPath] = input
	}
	parsed, err := querytemplate.Parse(query.Function, string(input.data))
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
