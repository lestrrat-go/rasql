package schemagen

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lestrrat-go/rasql/schema"
	"golang.org/x/tools/go/packages"
)

type BindingSetOptions struct {
	Dir      string
	Reserved []string
	resolver packageNameResolver
}

type packageNameResolver interface {
	Name(directory, importPath string) (string, error)
}

type goPackagesNameResolver struct{}

func (goPackagesNameResolver) Name(directory, importPath string) (string, error) {
	key := directory + "\x00" + importPath
	if value, ok := packageNames.Load(key); ok {
		return value.(string), nil
	}
	loaded, err := packages.Load(&packages.Config{Mode: packages.NeedName, Dir: directory}, importPath)
	if err != nil {
		return "", err
	}
	if len(loaded) != 1 || len(loaded[0].Errors) != 0 || !token.IsIdentifier(loaded[0].Name) {
		return "", fmt.Errorf("package %q has no valid package name", importPath)
	}
	packageNames.Store(key, loaded[0].Name)
	return loaded[0].Name, nil
}

type BindingRef struct {
	key      string
	nullable string
	resolved ResolvedBinding
}

type BindingSet struct {
	options   BindingSetOptions
	refs      []BindingRef
	imports   map[string]schema.GoImport
	aliases   map[string]string
	finalized bool
}

var packageNames sync.Map

func NewBindingSet(options BindingSetOptions) *BindingSet {
	return &BindingSet{options: options, imports: make(map[string]schema.GoImport), aliases: make(map[string]string)}
}

func (s *BindingSet) Add(column schema.ColumnDef) (BindingRef, error) {
	if s.finalized {
		return BindingRef{}, fmt.Errorf("schemagen: binding set is finalized")
	}
	resolved, err := resolveBindingPackagesWithResolver(column, s.options.Dir, s.options.resolver)
	if err != nil {
		return BindingRef{}, err
	}
	key, nullable, err := canonicalBindingTypes(resolved)
	if err != nil {
		return BindingRef{}, fmt.Errorf("generate: column %q GoBinding: %w", column.Name, err)
	}
	for _, imported := range resolved.Imports {
		if old, ok := s.imports[imported.Path]; ok && old.Name != imported.Name {
			return BindingRef{}, fmt.Errorf("generate: import path %q has conflicting aliases %q and %q", imported.Path, old.Name, imported.Name)
		}
		s.imports[imported.Path] = imported
	}
	ref := BindingRef{key: key, nullable: nullable, resolved: resolved}
	s.refs = append(s.refs, ref)
	return ref, nil
}

func (s *BindingSet) Finalize() error {
	if s.finalized {
		return nil
	}
	paths := make([]string, 0, len(s.imports))
	for path := range s.imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	used := make(map[string]struct{}, len(s.options.Reserved))
	for _, name := range s.options.Reserved {
		used[name] = struct{}{}
	}
	for _, path := range paths {
		imported := s.imports[path]
		name := imported.Name
		if name == "" {
			name = packageNameFor(imported.Path, s.options.Dir)
		}
		if name == "" {
			return fmt.Errorf("generate: cannot resolve package name for import %q", path)
		}
		base := name
		for n := 2; ; n++ {
			if _, ok := used[name]; !ok {
				break
			}
			name = base + strconv.Itoa(n)
		}
		used[name] = struct{}{}
		s.aliases[path] = name
		imported.Name = name
		s.imports[path] = imported
	}
	s.finalized = true
	return nil
}

func (s *BindingSet) Type(ref BindingRef, nullable bool) (string, error) {
	if !s.finalized {
		return "", fmt.Errorf("schemagen: binding set is not finalized")
	}
	expression := ref.resolved.For(nullable)
	return rewriteBindingExpression(expression, ref.resolved.Imports, s.aliases)
}

func (s *BindingSet) Imports() []schema.GoImport {
	paths := make([]string, 0, len(s.imports))
	for path := range s.imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]schema.GoImport, 0, len(paths))
	for _, path := range paths {
		result = append(result, s.imports[path])
	}
	return result
}

func SameBindingType(left, right BindingRef, nullable bool) bool {
	if nullable {
		return left.nullable == right.nullable
	}
	return left.key == right.key
}

func sameColumnBindingType(left, right schema.ColumnDef) (string, bool) {
	l, err := resolveBindingPackages(left, "")
	if err != nil {
		return "", false
	}
	r, err := resolveBindingPackages(right, "")
	if err != nil {
		return "", false
	}
	leftKey, _, err := canonicalBindingTypes(l)
	if err != nil {
		return "", false
	}
	rightKey, _, err := canonicalBindingTypes(r)
	if err != nil || leftKey != rightKey {
		return "", false
	}
	return l.For(false), true
}

func packageNameFor(path, dir string) string {
	name, _ := (goPackagesNameResolver{}).Name(dir, path)
	return name
}

func resolveBindingPackages(column schema.ColumnDef, dir string) (ResolvedBinding, error) {
	return resolveBindingPackagesWithResolver(column, dir, nil)
}

func resolveBindingPackagesWithResolver(column schema.ColumnDef, dir string, resolver packageNameResolver) (ResolvedBinding, error) {
	resolved, err := ResolveGoBinding(column)
	if err != nil {
		return ResolvedBinding{}, err
	}
	for i, imported := range resolved.Imports {
		if imported.Name == "" {
			if resolver == nil {
				resolver = goPackagesNameResolver{}
			}
			name, resolveErr := resolver.Name(dir, imported.Path)
			if resolveErr != nil {
				return ResolvedBinding{}, fmt.Errorf("generate: cannot resolve package name for import %q: %w", imported.Path, resolveErr)
			}
			if name == "" {
				return ResolvedBinding{}, fmt.Errorf("generate: cannot resolve package name for import %q", imported.Path)
			}
			resolved.Imports[i].Name = name
		}
	}
	return resolved, nil
}

func canonicalBindingTypes(binding ResolvedBinding) (string, string, error) {
	key, err := canonicalBindingType(binding.Type, binding.Imports)
	if err != nil {
		return "", "", err
	}
	nullable := binding.NullableType
	if nullable == "" {
		nullable = "*" + binding.Type
	}
	nullableKey, err := canonicalBindingType(nullable, binding.Imports)
	return key, nullableKey, err
}

func canonicalBindingType(expression string, imports []schema.GoImport) (string, error) {
	return rewriteBindingExpressionWithNames(expression, imports, func(path string) string { return "__rasql_import_" + strconv.Itoa(indexOfImport(path, imports)) })
}

func indexOfImport(path string, imports []schema.GoImport) int {
	for i, imported := range imports {
		if imported.Path == path {
			return i
		}
	}
	return -1
}

func rewriteBindingExpression(expression string, imports []schema.GoImport, aliases map[string]string) (string, error) {
	return rewriteBindingExpressionWithNames(expression, imports, func(path string) string { return aliases[path] })
}

func rewriteBindingExpressionWithNames(expression string, imports []schema.GoImport, alias func(string) string) (string, error) {
	expr, err := parser.ParseExpr(expression)
	if err != nil {
		return "", err
	}
	byName := make(map[string]string, len(imports))
	for _, imported := range imports {
		if previous, ok := byName[imported.Name]; ok && previous != imported.Path {
			return "", fmt.Errorf("duplicate import name %q", imported.Name)
		}
		byName[imported.Name] = imported.Path
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if path, ok := byName[ident.Name]; ok {
			ident.Name = alias(path)
		}
		return true
	})
	var output strings.Builder
	if err := format.Node(&output, token.NewFileSet(), expr); err != nil {
		return "", err
	}
	return output.String(), nil
}

type bindingState struct {
	imports []schema.GoImport
	aliases map[string]string
}

func newBindingState(tables []schema.TableDef) bindingState {
	// Kept for the table generator compatibility path; new query generation uses BindingSet.
	used := map[string]struct{}{"fmt": {}, "time": {}, "context": {}, "rasql": {}, "schema": {}, "sqltext": {}}
	paths := make(map[string]schema.GoImport)
	for _, table := range tables {
		for _, column := range table.Columns {
			if column.GoBinding == nil {
				continue
			}
			for _, imported := range column.GoBinding.Imports {
				paths[imported.Path] = imported
			}
		}
	}
	ordered := make([]schema.GoImport, 0, len(paths))
	for _, imported := range paths {
		ordered = append(ordered, imported)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	aliased := ordered[:0]
	for _, imported := range ordered {
		name := imported.Name
		if name == "" {
			parts := strings.Split(imported.Path, "/")
			name = parts[len(parts)-1]
		}
		base := name
		for suffix := 2; ; suffix++ {
			if _, exists := used[name]; !exists {
				break
			}
			name = base + strconv.Itoa(suffix)
		}
		used[name] = struct{}{}
		imported.Name = name
		aliased = append(aliased, imported)
	}
	ordered = aliased
	aliases := make(map[string]string, len(ordered))
	for _, imported := range ordered {
		aliases[imported.Path] = imported.Name
	}
	return bindingState{imports: ordered, aliases: aliases}
}

func (b bindingState) typeFor(column schema.ColumnDef, nullable bool) string {
	resolved, _ := ResolveGoBinding(column)
	typeName := resolved.For(nullable)
	if column.GoBinding == nil {
		return typeName
	}
	for _, imported := range column.GoBinding.Imports {
		original := imported.Name
		if original == "" {
			parts := strings.Split(imported.Path, "/")
			original = parts[len(parts)-1]
		}
		if alias := b.aliases[imported.Path]; alias != "" && alias != original {
			typeName = rewriteTypeSelector(typeName, original, alias)
		}
	}
	return typeName
}

func rewriteTypeSelector(expression, from, to string) string {
	expr, err := parser.ParseExpr(expression)
	if err != nil {
		return expression
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok {
			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == from {
				ident.Name = to
			}
		}
		return true
	})
	var output strings.Builder
	if err := format.Node(&output, token.NewFileSet(), expr); err != nil {
		return expression
	}
	return output.String()
}

// RewriteBindingType rewrites an import selector in a parsed Go type expression.
func RewriteBindingType(expression, from, to string) string {
	return rewriteTypeSelector(expression, from, to)
}

// ResolvedBinding is the validated Go type and imports for one column.
type ResolvedBinding struct {
	Type         string
	NullableType string
	Imports      []schema.GoImport
}

// For returns the generated type for the requested nullability.
func (b ResolvedBinding) For(nullable bool) string {
	if !nullable {
		return b.Type
	}
	if b.NullableType != "" {
		return b.NullableType
	}
	if strings.HasPrefix(b.Type, "*") {
		return b.Type
	}
	return "*" + b.Type
}

// ResolveGoBinding validates and resolves a column's configured binding.
func ResolveGoBinding(column schema.ColumnDef) (ResolvedBinding, error) {
	base := ColumnGoType(column)
	if column.GoBinding == nil {
		resolved := ResolvedBinding{Type: base}
		if column.Nullable && (column.Type.Kind() == schema.KindBytes || column.Type.Kind() == schema.KindJSON) {
			resolved.NullableType = base
		}
		return resolved, nil
	}
	b := column.GoBinding
	if strings.TrimSpace(b.Type) == "" {
		return ResolvedBinding{}, fmt.Errorf("generate: column %q GoBinding.Type is blank", column.Name)
	}
	if err := validateTypeExpression(b.Type, b.Imports); err != nil {
		return ResolvedBinding{}, fmt.Errorf("generate: column %q GoBinding.Type: %w", column.Name, err)
	}
	if b.NullableType != "" {
		if err := validateTypeExpression(b.NullableType, b.Imports); err != nil {
			return ResolvedBinding{}, fmt.Errorf("generate: column %q GoBinding.NullableType: %w", column.Name, err)
		}
	}
	imports := append([]schema.GoImport(nil), b.Imports...)
	if err := validateImports(imports, b.Type, b.NullableType); err != nil {
		return ResolvedBinding{}, fmt.Errorf("generate: column %q GoBinding: %w", column.Name, err)
	}
	sort.Slice(imports, func(i, j int) bool {
		if imports[i].Path == imports[j].Path {
			return imports[i].Name < imports[j].Name
		}
		return imports[i].Path < imports[j].Path
	})
	return ResolvedBinding{Type: b.Type, NullableType: b.NullableType, Imports: imports}, nil
}

func validateTypeExpression(expression string, imports []schema.GoImport) error {
	expr, err := parser.ParseExpr(expression)
	if err != nil {
		return fmt.Errorf("type expression does not parse: %w", err)
	}
	used := make(map[string]struct{})
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := selector.X.(*ast.Ident); ok {
			used[ident.Name] = struct{}{}
		}
		return true
	})
	known := make(map[string]struct{}, len(imports))
	for _, imported := range imports {
		name := imported.Name
		if name == "" {
			parts := strings.Split(imported.Path, "/")
			name = parts[len(parts)-1]
		}
		if name == "_" || name == "." {
			return fmt.Errorf("import %q uses unsupported name %q", imported.Path, name)
		}
		if _, exists := known[name]; exists {
			return fmt.Errorf("duplicate import name %q", name)
		}
		known[name] = struct{}{}
	}
	for name := range used {
		if _, ok := known[name]; !ok {
			return fmt.Errorf("selector %q has no matching import", name)
		}
	}
	return nil
}

func validateImports(imports []schema.GoImport, expressions ...string) error {
	paths := make(map[string]struct{}, len(imports))
	used := make(map[string]struct{}, len(imports))
	for _, imported := range imports {
		if strings.TrimSpace(imported.Path) == "" {
			return fmt.Errorf("import path is blank")
		}
		if imported.Name == "_" || imported.Name == "." {
			return fmt.Errorf("import %q uses unsupported name %q", imported.Path, imported.Name)
		}
		if _, exists := paths[imported.Path]; exists {
			return fmt.Errorf("duplicate import path %q", imported.Path)
		}
		paths[imported.Path] = struct{}{}
	}
	for _, expression := range expressions {
		if expression == "" {
			continue
		}
		if err := validateTypeExpression(expression, imports); err != nil {
			return err
		}
		expr, err := parser.ParseExpr(expression)
		if err != nil {
			return err
		}
		ast.Inspect(expr, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok {
				if ident, ok := selector.X.(*ast.Ident); ok {
					used[ident.Name] = struct{}{}
				}
			}
			return true
		})
	}
	for _, imported := range imports {
		name := imported.Name
		if name == "" {
			parts := strings.Split(imported.Path, "/")
			name = parts[len(parts)-1]
		}
		if _, ok := used[name]; !ok {
			return fmt.Errorf("import %q is unused", imported.Path)
		}
	}
	return nil
}
