// Package namedsql compiles SQL written with named bind actions into a
// statement carrying one dialect's placeholders, with the bound values in
// placeholder order. The only action it accepts is {{bind "name"}}, so a
// value can never become SQL text.
package namedsql

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Template is SQL text containing {{bind "name"}} actions.
type Template struct {
	name        string
	parts       []templatePart
	parameters  []string
	uniqueNames []string
}

type templatePart struct {
	text       string
	bindIndex  int
	isBindPart bool
}

// Parse validates source and returns a restricted SQL template.
func Parse(name string, source string) (Template, error) {
	if name == "" {
		return Template{}, fmt.Errorf("namedsql: name must not be empty")
	}
	if strings.TrimSpace(source) == "" {
		return Template{}, fmt.Errorf("namedsql %q: source must not be empty", name)
	}

	parts := make([]templatePart, 0)
	parameters := make([]string, 0)
	uniqueNames := make([]string, 0)
	seen := make(map[string]struct{})
	remaining := source
	for remaining != "" {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			parts = append(parts, templatePart{text: remaining})
			break
		}
		literal := remaining[:start]
		if literal != "" {
			parts = append(parts, templatePart{text: literal})
		}
		remaining = remaining[start+2:]
		end := strings.Index(remaining, "}}")
		if end < 0 {
			return Template{}, fmt.Errorf("namedsql %q: unclosed action", name)
		}
		parameter, err := parseBindAction(strings.TrimSpace(remaining[:end]))
		if err != nil {
			return Template{}, fmt.Errorf("namedsql %q: %w", name, err)
		}
		bindIndex := len(parameters)
		parts = append(parts, templatePart{bindIndex: bindIndex, isBindPart: true})
		parameters = append(parameters, parameter)
		if _, exists := seen[parameter]; !exists {
			seen[parameter] = struct{}{}
			uniqueNames = append(uniqueNames, parameter)
		}
		remaining = remaining[end+2:]
	}
	return Template{name: name, parts: parts, parameters: parameters, uniqueNames: uniqueNames}, nil
}

func parseBindAction(action string) (string, error) {
	fields := strings.Fields(action)
	if len(fields) != 2 || fields[0] != "bind" {
		return "", fmt.Errorf("actions must use bind with one quoted parameter name")
	}
	name, err := strconv.Unquote(fields[1])
	if err != nil {
		return "", fmt.Errorf("parameter name must be quoted")
	}
	if err := schema.ValidateIdentifier(name); err != nil {
		return "", fmt.Errorf("invalid parameter name: %w", err)
	}
	return name, nil
}

// isNilDialect reports whether d is nil, including a typed nil held in a
// non-nil dialect.Dialect interface value (for example a nil *T pointer
// implementation).
func isNilDialect(d dialect.Dialect) bool {
	if d == nil {
		return true
	}
	value := reflect.ValueOf(d)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Compile renders template placeholders for d.
func (t Template) Compile(d dialect.Dialect) (Compiled, error) {
	if isNilDialect(d) {
		return Compiled{}, fmt.Errorf("namedsql %q: dialect must not be nil", t.name)
	}
	if t.name == "" || len(t.parts) == 0 {
		return Compiled{}, fmt.Errorf("namedsql: invalid template")
	}
	placeholders := make([]string, len(t.parameters))
	for index := range t.parameters {
		placeholder, err := d.Placeholder(index + 1)
		if err != nil {
			return Compiled{}, fmt.Errorf("namedsql %q: placeholder %d: %w", t.name, index+1, err)
		}
		placeholders[index] = placeholder
	}

	return Compiled{
		name:        t.name,
		sql:         renderTemplateParts(t.parts, placeholders),
		parameters:  append([]string(nil), t.parameters...),
		uniqueNames: append([]string(nil), t.uniqueNames...),
	}, nil
}

// Compiled is a template with dialect-specific placeholders.
type Compiled struct {
	name        string
	sql         string
	parameters  []string
	uniqueNames []string
}

// SQL returns static SQL containing placeholders.
func (c Compiled) SQL() string {
	return c.sql
}

// ParameterNames yields the unique parameter names in first-use order.
func (c Compiled) ParameterNames() iter.Seq[string] {
	return slices.Values(c.uniqueNames)
}

// Bind supplies all named values and returns a parameterized statement.
func (c Compiled) Bind(values map[string]any) (render.Statement, error) {
	if c.name == "" || strings.TrimSpace(c.sql) == "" {
		return render.Statement{}, fmt.Errorf("namedsql: invalid compiled template")
	}
	args := make([]any, len(c.parameters))
	for index, name := range c.parameters {
		value, ok := values[name]
		if !ok {
			return render.Statement{}, fmt.Errorf("namedsql %q: missing value for %q", c.name, name)
		}
		args[index] = value
	}
	for name := range values {
		if !contains(c.uniqueNames, name) {
			return render.Statement{}, fmt.Errorf("namedsql %q: unused value %q", c.name, name)
		}
	}
	statement, err := render.Precompiled(c.sql, args...)
	if err != nil {
		return render.Statement{}, fmt.Errorf("namedsql %q: %w", c.name, err)
	}
	return statement, nil
}

// GoSource returns a Go function that creates this static statement.
func (c Compiled) GoSource(packageName string, functionName string) ([]byte, error) {
	if c.name == "" || strings.TrimSpace(c.sql) == "" {
		return nil, fmt.Errorf("namedsql: invalid compiled template")
	}
	if !isUsableGoIdentifier(packageName) {
		return nil, fmt.Errorf("namedsql: invalid package name %q", packageName)
	}
	if !isUsableGoIdentifier(functionName) {
		return nil, fmt.Errorf("namedsql %q: invalid function name %q", c.name, functionName)
	}
	if functionName == "init" || (packageName == "main" && functionName == "main") {
		return nil, fmt.Errorf("namedsql %q: function name %q cannot be generated in package %q", c.name, functionName, packageName)
	}
	reservedNames := map[string]struct{}{
		packageName:  {},
		functionName: {},
	}
	for _, name := range c.uniqueNames {
		if !isUsableGoIdentifier(name) {
			return nil, fmt.Errorf("namedsql %q: parameter %q cannot be a Go identifier", c.name, name)
		}
		reservedNames[name] = struct{}{}
	}
	renderName := availableGoIdentifier("rasqlrender", reservedNames)
	parameterType := "any"
	if functionName == "any" {
		parameterType = "interface{}"
	}
	errorType := "error"
	if functionName == "error" {
		errorType = "interface{ Error() string }"
	}

	var source bytes.Buffer
	source.WriteString("// Code generated by rasqlgen; DO NOT EDIT.\n\n")
	source.WriteString("package ")
	source.WriteString(packageName)
	source.WriteString("\n\nimport ")
	source.WriteString(renderName)
	source.WriteString(" \"github.com/lestrrat-go/rasql/render\"\n\n")
	source.WriteString("func ")
	source.WriteString(functionName)
	source.WriteByte('(')
	for index, name := range c.uniqueNames {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteString(name)
		source.WriteByte(' ')
		source.WriteString(parameterType)
	}
	source.WriteString(") (")
	source.WriteString(renderName)
	source.WriteString(".Statement, ")
	source.WriteString(errorType)
	source.WriteString(") {\n\treturn ")
	source.WriteString(renderName)
	source.WriteString(".Precompiled(")
	source.WriteString(strconv.Quote(c.sql))
	for _, name := range c.parameters {
		source.WriteString(", ")
		source.WriteString(name)
	}
	source.WriteString(")\n}\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("namedsql %q: format source: %w", c.name, err)
	}
	return formatted, nil
}

func isUsableGoIdentifier(name string) bool {
	return name != "_" && token.IsIdentifier(name)
}

func availableGoIdentifier(base string, reserved map[string]struct{}) string {
	for index := 0; ; index++ {
		name := base
		if index > 0 {
			name += strconv.Itoa(index)
		}
		if _, exists := reserved[name]; !exists {
			return name
		}
	}
}

func renderTemplateParts(parts []templatePart, placeholders []string) string {
	var sql strings.Builder
	for _, part := range parts {
		if part.isBindPart {
			sql.WriteString(placeholders[part.bindIndex])
			continue
		}
		sql.WriteString(part.text)
	}
	return sql.String()
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
