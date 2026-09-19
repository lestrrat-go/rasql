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

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

type compactFile struct {
	name         string
	source       []byte
	declarations []string
}

type compactStoreInput struct {
	files []compactFile
}

// RenderCompact renders canonical compiler input into a Store using the
// compact common query/runtime contracts.
func RenderCompact(in EmitterInput) (Store, error) {
	if in.generation.Package == "" {
		return Store{}, errors.New("generate: compact renderer requires an input from NewEmitterInput")
	}
	copy := in.clone()
	if copy.generation.Emitter != "compact" {
		return Store{}, errors.New("generate: compact renderer requires generation.emitter compact")
	}
	for _, object := range copy.goModel.Objects {
		for _, column := range object.Columns {
			if !column.Nullable {
				continue
			}
			for _, mapping := range copy.mappings.Scalars {
				if mapping.Name == column.Scalar && mapping.NullableGoType != "" && !strings.HasPrefix(mapping.NullableGoType, "rasql.Nullable[") {
					return Store{}, fmt.Errorf("generate: compact %s.%s uses unsupported distinct nullable Go type %q", object.ID, column.Name, mapping.NullableGoType)
				}
			}
		}
	}
	tables, diagnostics := compilerir.TableDefsFromPhysical(copy.catalog)
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return Store{}, fmt.Errorf("generate: compact: %s", diagnostic.Message)
		}
	}
	byID := make(map[compilerir.ObjectID]compilerir.PhysicalObject, len(copy.catalog.Objects))
	for _, object := range copy.catalog.Objects {
		byID[object.ID] = object
	}
	semantic := make(map[compilerir.ObjectID]compilerir.SemanticObject, len(copy.semantic.Objects))
	for _, object := range copy.semantic.Objects {
		semantic[object.ID] = object
	}
	goObjects := make(map[compilerir.ObjectID]compilerir.GoObject, len(copy.goModel.Objects))
	for _, object := range copy.goModel.Objects {
		goObjects[object.ID] = object
	}
	configs := make(map[compilerir.ObjectID]compilerir.ObjectGoName, len(copy.generation.Objects))
	for _, config := range copy.generation.Objects {
		configs[config.ID] = config
	}
	columnBindings := make(map[compilerir.ObjectID][]compilerir.ColumnGoBinding, len(copy.generation.ColumnBindings))
	for _, binding := range copy.generation.ColumnBindings {
		columnBindings[binding.Object] = append(columnBindings[binding.Object], binding)
	}
	tablesByID := make(map[compilerir.ObjectID]schema.TableDef, len(copy.catalog.Objects))
	for _, object := range copy.catalog.Objects {
		if table, ok := findCompactTable(tables, object); ok {
			tablesByID[object.ID] = table
		}
	}
	targets := make(map[compilerir.ObjectID]schemagen.CompactObjectRef, len(copy.catalog.Objects))
	for _, object := range copy.catalog.Objects {
		targets[object.ID] = schemagen.CompactObjectRef{
			Catalog: object, Semantic: semantic[object.ID], Go: goObjects[object.ID],
			Generation: configs[object.ID], Table: tablesByID[object.ID],
			ColumnBindings: columnBindings[object.ID],
		}
	}
	files := make([]compactFile, 0, len(copy.catalog.Objects)+2)
	seenFiles := make(map[string]string)
	seenDecls := make(map[string]string)
	for _, object := range copy.catalog.Objects {
		config := configs[object.ID]
		if config.File == "" {
			return Store{}, fmt.Errorf("generate: compact object %q has no output file", object.ID)
		}
		table, ok := findCompactTable(tables, object)
		if !ok {
			return Store{}, fmt.Errorf("generate: compact object %q has no descriptor", object.ID)
		}
		source, err := schemagen.CompactObjectSource(copy.generation.Package, schemagen.CompactObject{
			Catalog: object, Semantic: semantic[object.ID], Go: goObjects[object.ID],
			Generation: config, Table: table, Mappings: copy.generation.Scalars,
			ColumnBindings: columnBindings[object.ID], Targets: targets,
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
	meta := []byte(compactMetadataSource(copy.generation.Package))
	test := []byte(genfile.Marker + "\n\npackage " + copy.generation.Package + "\n")
	metaDeclarations, err := compactDeclarations(meta)
	if err != nil {
		return Store{}, fmt.Errorf("generate: compact metadata: %w", err)
	}
	for _, declaration := range metaDeclarations {
		if owner, exists := seenDecls[declaration]; exists {
			return Store{}, fmt.Errorf("generate: compact declaration %q collides with %s", declaration, owner)
		}
		seenDecls[declaration] = schemaDescriptorFilename
	}
	files = append(files, compactFile{name: schemaDescriptorFilename, source: meta, declarations: metaDeclarations})
	files = append(files, compactFile{name: schemaDescriptorTestFilename, source: test})
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return Store{
		Package: copy.generation.Package,
		Dir:     copy.generation.Output,
		Prune:   copy.generation.Prune,
		// RenderCompact owns schema declarations from EmitterInput. PlanContext
		// appends configured SQL from Store.TypedQueries and checks it with the
		// same file and identifier ledgers.
		compact: &compactStoreInput{files: cloneCompactFiles(files)},
	}, nil
}

func compactMetadataSource(packageName string) string {
	return genfile.Marker + "\n\npackage " + packageName + `

import (
	"fmt"

	"github.com/lestrrat-go/rasql"
)

func rasqlgenBind[S, C any](sticky *error, source S, name, codec string, bind func(S, string, string) (C, error)) C {
	if *sticky != nil {
		var zero C
		return zero
	}
	value, err := bind(source, name, codec)
	if err != nil {
		*sticky = err
	}
	return value
}

// rasqlgenColumn binds one column of a generated table and drops the error the
// bind reports. Every caller passes a table built from the descriptor this
// package generated beside it, which holds the name, the Go type and the
// nullability the bind checks, so the bind fails for one input only: a zero
// table value, which carries no columns at all. That leaves a zero column
// whose statement reports the missing table when it builds, which is where a
// zero table is reported anyway.
func rasqlgenColumn[S, C any](source S, name, codec string, bind func(S, string, string) (C, error)) C {
	value, _ := bind(source, name, codec)
	return value
}

func rasqlgenAppendMutationField[R any](fields []rasql.MutationField[R], field rasql.MutationField[R]) []rasql.MutationField[R] {
	return append(append([]rasql.MutationField[R](nil), fields...), field)
}

func rasqlgenResultSchema(columns []rasql.ResultColumn) rasql.ResultSchema {
	value, err := rasql.NewResultSchema(columns...)
	if err != nil {
		panic(err)
	}
	return value
}

func rasqlgenOptionalResultSchema(columns []rasql.ResultColumn) rasql.ResultSchema {
	for i := range columns {
		columns[i].Nullable = true
	}
	return rasqlgenResultSchema(columns)
}

func rasqlgenAssignNullable[T any](source rasql.Nullable[T], target *T) {
	if source.Valid {
		*target = source.Value
	}
}

func rasqlgenPageKey[R, T comparable](direction rasql.PageDirection, value rasql.Expr[T], extract func(R) T) (rasql.PageKey[R], error) {
	switch direction {
	case rasql.PageAscending:
		return rasql.AscKey(value, extract), nil
	case rasql.PageDescending:
		return rasql.DescKey(value, extract), nil
	default:
		return nil, fmt.Errorf("invalid page direction %d", direction)
	}
}

func rasqlgenNullablePageKey[R, T comparable](direction rasql.PageDirection, value rasql.NullExpr[T], extract func(R) rasql.Nullable[T], nulls rasql.NullOrder) (rasql.PageKey[R], error) {
	if nulls == rasql.NullOrderDefault {
		return nil, fmt.Errorf("nullable page keys require explicit NULL order")
	}
	switch direction {
	case rasql.PageAscending:
		return rasql.AscNullKey(value, extract, nulls), nil
	case rasql.PageDescending:
		return rasql.DescNullKey(value, extract, nulls), nil
	default:
		return nil, fmt.Errorf("invalid page direction %d", direction)
	}
}
`
}

func findCompactTable(tables []schema.TableDef, object compilerir.PhysicalObject) (schema.TableDef, bool) {
	for _, table := range tables {
		if table.Schema == object.Schema && table.Name == object.Name {
			return table, true
		}
	}
	return schema.TableDef{}, false
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
	for index, query := range s.TypedQueries {
		file, err := s.planTypedQuery(dir, query, filenames, identifiers)
		if err != nil {
			return Plan{}, fmt.Errorf("generate: compact typed query[%d]: %w", index, err)
		}
		declarations, err := compactDeclarations(file.Source)
		if err != nil {
			return Plan{}, fmt.Errorf("generate: compact typed query[%d]: parse declarations: %w", index, err)
		}
		owner := fmt.Sprintf("query %q", query.Function)
		queryDeclarations := make(map[string]struct{}, len(declarations))
		for _, declaration := range declarations {
			if _, duplicate := queryDeclarations[declaration]; duplicate {
				return Plan{}, fmt.Errorf("generate: compact typed query[%d] declaration %q is emitted more than once", index, declaration)
			}
			queryDeclarations[declaration] = struct{}{}
			if existing, exists := identifiers[declaration]; exists {
				if !strings.Contains(existing, owner) {
					return Plan{}, fmt.Errorf("generate: compact typed query[%d] declaration %q collides with %s", index, declaration, existing)
				}
				continue
			}
			identifiers[declaration] = owner + " declaration"
		}
		files = append(files, file)
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
