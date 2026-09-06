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

	"github.com/lestrrat-go/rasql/schema"
)

type bindingState struct {
	imports []schema.GoImport
	aliases map[string]string
}

func newBindingState(tables []schema.TableDef) bindingState {
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
