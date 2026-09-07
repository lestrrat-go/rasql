package compilerir

import (
	"fmt"
	"go/ast"
	"go/parser"
	"regexp"
	"strings"
)

var mappingCodecPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// DefaultScalarMapping returns the canonical mapping for a portable scalar.
func DefaultScalarMapping(scalar string) (ScalarMapping, bool) {
	typeName := map[string]string{
		"boolean": "bool", "integer": "int64", "float": "float64",
		"unsigned_integer": "uint64",
		"text":             "string", "bytes": "[]byte", "time": "time.Time",
		"json": "[]byte", "uuid": "string", "decimal": "string",
	}[scalar]
	if typeName == "" {
		return ScalarMapping{}, false
	}
	return ScalarMapping{Name: scalar, GoType: typeName}, true
}

// ValidateMappingConfig checks mapping names, match selectors, Go expressions,
// imports, and codec references. Runtime codec availability is checked by the
// configured registry at execution time; the stable name is validated here.
func ValidateMappingConfig(config MappingConfig, packageName string) error {
	seen := make(map[string]struct{}, len(config.Scalars))
	for i, mapping := range config.Scalars {
		path := fmt.Sprintf("scalars[%d]", i)
		if mapping.Name == "" {
			return fmt.Errorf("%s.name: must not be empty", path)
		}
		if _, ok := seen[mapping.Name]; ok {
			return fmt.Errorf("%s.name: duplicate scalar %q", path, mapping.Name)
		}
		seen[mapping.Name] = struct{}{}
		if mapping.GoType == "" {
			return fmt.Errorf("%s.go_type: must not be empty", path)
		}
		expressions := []string{mapping.GoType}
		if mapping.NullableGoType != "" {
			expressions = append(expressions, mapping.NullableGoType)
		}
		if err := validateGoExpressions(expressions, mapping.Imports, packageName); err != nil {
			return fmt.Errorf("%s.go_type: %w", path, err)
		}
		if mapping.Codec == "" {
			return fmt.Errorf("%s.codec: must not be empty", path)
		}
		if !mappingCodecPattern.MatchString(mapping.Codec) {
			return fmt.Errorf("%s.codec: malformed codec identifier", path)
		}
		if mapping.Match.Dialect == "" && mapping.Match.Schema == "" && mapping.Match.Name == "" && mapping.Match.Kind == "" && mapping.Match.LogicalKind == "" {
			return fmt.Errorf("%s.match: must select a native type or logical kind", path)
		}
		if mapping.Match.Dialect == "" && mapping.Match.Schema != "" {
			return fmt.Errorf("%s.match.schema: requires dialect", path)
		}
		if mapping.Match.Schema != "" && mapping.Match.Name == "" {
			return fmt.Errorf("%s.match.schema: requires name", path)
		}
	}
	return nil
}

type mappingSelection struct {
	scalar    string
	mapping   ScalarMapping
	found     bool
	ambiguous bool
}

func importsForType(expression string, imports []GoImport) []GoImport {
	expr, err := parser.ParseExpr(expression)
	if err != nil {
		return nil
	}
	used := map[string]struct{}{}
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
	result := make([]GoImport, 0, len(imports))
	for _, imp := range imports {
		name := imp.Alias
		if name == "" {
			parts := strings.Split(imp.Path, "/")
			name = parts[len(parts)-1]
		}
		if _, ok := used[name]; ok {
			result = append(result, imp)
		}
	}
	return result
}

func selectMapping(column PhysicalColumn, config MappingConfig) mappingSelection {
	bestRank := -1
	var selected ScalarMapping
	matches := 0
	for _, mapping := range config.Scalars {
		rank, ok := mappingRank(column, mapping.Match)
		if !ok {
			continue
		}
		if rank > bestRank {
			bestRank, selected, matches = rank, mapping, 1
			continue
		}
		if rank == bestRank {
			matches++
		}
	}
	if bestRank < 0 {
		if column.LogicalKind == "integer" && column.Integer != nil && column.Integer.Unsigned {
			mapping := ScalarMapping{Name: "unsigned_integer", GoType: "uint64"}
			return mappingSelection{scalar: mapping.Name, mapping: mapping, found: true}
		}
		if mapping, ok := DefaultScalarMapping(column.LogicalKind); ok {
			return mappingSelection{scalar: mapping.Name, mapping: mapping, found: true}
		}
		return mappingSelection{}
	}
	return mappingSelection{scalar: selected.Name, mapping: selected, found: matches == 1, ambiguous: matches > 1}
}

func mappingRank(column PhysicalColumn, match NativeMatch) (int, bool) {
	if match.LogicalKind != "" && match.LogicalKind != column.LogicalKind {
		return 0, false
	}
	if match.Dialect == "" && match.Schema == "" && match.Name == "" && match.Kind == "" {
		if match.LogicalKind == "" {
			return 0, false
		}
		return 1, true
	}
	if column.Native == nil {
		return 0, false
	}
	n := column.Native
	if match.Dialect != "" && match.Dialect != n.Dialect || match.Schema != "" && match.Schema != n.Schema || match.Name != "" && match.Name != n.Name || match.Kind != "" && match.Kind != n.Kind {
		return 0, false
	}
	if match.Schema != "" && match.Name != "" {
		return 4, true
	}
	return 3, true
}
