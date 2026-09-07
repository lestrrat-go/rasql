package generate

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

// APIMapping records one declaration's transition from legacy output to the
// compact surface. It is kept in the held render snapshot for diagnostics.
type APIMapping struct {
	Legacy  string `json:"legacy"`
	Compact string `json:"compact"`
	Status  string `json:"status"`
}

type compactFile struct {
	name         string
	source       []byte
	declarations []string
}

type compactStoreInput struct {
	input    EmitterInput
	files    []compactFile
	manifest []APIMapping
}

// RenderCompact renders canonical compiler input into a Store using the
// compact common query/runtime contracts.
func RenderCompact(in EmitterInput) (Store, error) {
	copy := in.Clone()
	if err := copy.Validate(); err != nil {
		return Store{}, err
	}
	if copy.Generation.Emitter != "compact" {
		return Store{}, errors.New("generate: compact renderer requires generation.emitter compact")
	}
	tables, diagnostics := compilerir.TableDefsFromPhysical(copy.Catalog)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Store{}, fmt.Errorf("generate: compact: %s", diagnostic.Message)
		}
	}
	byID := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(copy.Catalog.Objects))
	for _, object := range copy.Catalog.Objects {
		byID[object.ID] = object
	}
	semantic := make(map[compilerir.ObjectID]compilerir.SemanticObject, len(copy.Semantic.Objects))
	for _, object := range copy.Semantic.Objects {
		semantic[object.ID] = object
	}
	goObjects := make(map[compilerir.ObjectID]compilerir.GoObject, len(copy.Go.Objects))
	for _, object := range copy.Go.Objects {
		goObjects[object.ID] = object
	}
	configs := make(map[compilerir.ObjectID]compilerir.ObjectGoName, len(copy.Generation.Objects))
	for _, config := range copy.Generation.Objects {
		configs[config.ID] = config
	}
	defs := make(map[string]struct{ schemaName string })
	for _, table := range tables {
		defs[table.Schema+"\x00"+table.Name] = struct{ schemaName string }{schemaName: table.Name}
	}
	files := make([]compactFile, 0, len(copy.Catalog.Objects)+2)
	seenFiles := make(map[string]string)
	seenDecls := make(map[string]string)
	for _, object := range copy.Catalog.Objects {
		config := configs[object.ID]
		if config.File == "" {
			return Store{}, fmt.Errorf("generate: compact object %q has no output file", object.ID)
		}
		table, ok := findCompactTable(tables, object)
		if !ok {
			return Store{}, fmt.Errorf("generate: compact object %q has no descriptor", object.ID)
		}
		source, err := schemagen.CompactObjectSource(copy.Generation.Package, schemagen.CompactObject{
			Catalog: object, Semantic: semantic[object.ID], Go: goObjects[object.ID],
			Generation: config, Table: table, Mappings: copy.Generation.Scalars,
		})
		if err != nil {
			return Store{}, err
		}
		name := config.File
		if owner, exists := seenFiles[strings.ToLower(name)]; exists {
			return Store{}, fmt.Errorf("generate: compact file %q collides with %s", name, owner)
		}
		seenFiles[strings.ToLower(name)] = string(object.ID)
		declarations, err := compactDeclarations(source)
		if err != nil {
			return Store{}, fmt.Errorf("generate: compact file %q: %w", name, err)
		}
		for _, declaration := range declarations {
			if owner, exists := seenDecls[declaration]; exists {
				return Store{}, fmt.Errorf("generate: compact declaration %q collides with %s", declaration, owner)
			}
			seenDecls[declaration] = name
		}
		files = append(files, compactFile{name: name, source: append([]byte(nil), source...), declarations: declarations})
	}
	meta := []byte(genfile.Marker + "\n\npackage " + copy.Generation.Package + "\n")
	test := []byte(genfile.Marker + "\n\npackage " + copy.Generation.Package + "\n")
	files = append(files, compactFile{name: schemaDescriptorFilename, source: meta})
	files = append(files, compactFile{name: schemaDescriptorTestFilename, source: test})
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	manifest := compactManifest(copy, seenDecls)
	return Store{
		Package: copy.Generation.Package,
		Dir:     copy.Generation.Output,
		Prune:   copy.Generation.Prune,
		Dialect: compactDialect(copy.Catalog.Engine.Dialect),
		compact: &compactStoreInput{input: copy, files: cloneCompactFiles(files), manifest: append([]APIMapping(nil), manifest...)},
	}, nil
}

func findCompactTable(tables []schema.TableDef, object compilerir.PhysicalObject) (schema.TableDef, bool) {
	for _, table := range tables {
		if table.Schema == object.Schema && table.Name == object.Name {
			return table, true
		}
	}
	return schema.TableDef{}, false
}

func compactDialect(name string) dialect.Dialect {
	switch strings.ToLower(name) {
	case "postgres", "postgresql":
		return dialect.PostgreSQL()
	case "mysql":
		return dialect.MySQL()
	default:
		return dialect.SQLite()
	}
}

func cloneCompactFiles(files []compactFile) []compactFile {
	result := make([]compactFile, len(files))
	for i, file := range files {
		result[i] = compactFile{name: file.name, source: append([]byte(nil), file.source...), declarations: append([]string(nil), file.declarations...)}
	}
	return result
}

func compactDeclarations(source []byte) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "generated.go", source, 0)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Recv == nil {
				result = append(result, declaration.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					result = append(result, spec.Name.Name)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						result = append(result, name.Name)
					}
				}
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func compactManifest(in EmitterInput, declarations map[string]string) []APIMapping {
	result := make([]APIMapping, 0, len(declarations))
	for legacy, file := range declarations {
		result = append(result, APIMapping{Legacy: legacy, Compact: legacy, Status: "replacement"})
		_ = file
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Legacy != result[j].Legacy {
			return result[i].Legacy < result[j].Legacy
		}
		return result[i].Compact < result[j].Compact
	})
	return result
}

func (s Store) planCompactContext(ctx context.Context) (Plan, error) {
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
	checkRoot := root
	if checkRoot != "" {
		checkRoot, err = filepath.Abs(checkRoot)
		if err != nil {
			return Plan{}, fmt.Errorf("generate: resolve Root: %w", err)
		}
	}
	files := make([]File, 0, len(s.compact.files))
	filenames := make(map[string]string, len(s.compact.files))
	identifiers := make(map[string]string)
	for _, compact := range s.compact.files {
		if compact.name == "" || filepath.Base(compact.name) != compact.name || (!strings.HasSuffix(compact.name, "_gen.go") && !strings.HasSuffix(compact.name, "_gen_test.go")) {
			return Plan{}, fmt.Errorf("generate: compact output %q must be a generated Go file name", compact.name)
		}
		key := filenameKey(compact.name)
		if owner, exists := filenames[key]; exists {
			return Plan{}, fmt.Errorf("generate: compact output %q collides with %s", compact.name, owner)
		}
		filenames[key] = "compact output file"
		for _, declaration := range compact.declarations {
			if owner, exists := identifiers[declaration]; exists {
				return Plan{}, fmt.Errorf("generate: compact declaration %q collides with %s", declaration, owner)
			}
			identifiers[declaration] = compact.name
		}
		files = append(files, File{Path: filepath.Join(dir, compact.name), Source: append([]byte(nil), compact.source...)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return finishRenderedPlan(ctx, root, checkRoot, dir, s.Package, s.Prune, files)
}

func finishRenderedPlan(ctx context.Context, root, checkRoot, dir, packageName string, prune bool, files []File) (Plan, error) {
	if err := contextError(ctx); err != nil {
		return Plan{}, err
	}
	writes := make([]plannedWrite, 0, len(files))
	for i := range files {
		resolved, err := genfile.ResolveDestination(files[i].Path)
		if err != nil {
			return Plan{}, err
		}
		for _, write := range writes {
			if matchDestinations(write.destination, resolved) != distinctDestinations {
				return Plan{}, fmt.Errorf("generate: %s and %s both resolve to one destination", write.path, files[i].Path)
			}
		}
		files[i].Resolved = resolved
		writes = append(writes, plannedWrite{path: files[i].Path, destination: resolved})
	}
	if err := requireStorePackageOwnsDir(dir, packageName, files); err != nil {
		return Plan{}, err
	}
	orphans, dirInfo, err := findOrphans(dir, files)
	if err != nil {
		return Plan{}, fmt.Errorf("generate: scan %s for leftover files: %w", dir, err)
	}
	anchor, anchorInfo := dir, dirInfo
	if anchorInfo == nil {
		anchor, anchorInfo, err = deepestExistingDirectory(dir)
		if err != nil {
			return Plan{}, fmt.Errorf("generate: find an existing directory above %s: %w", dir, err)
		}
	}
	return Plan{files: files, orphans: orphans, dir: dir, prune: prune, packageName: packageName, root: checkRoot, anchor: anchor, anchorInfo: anchorInfo}, nil
}
