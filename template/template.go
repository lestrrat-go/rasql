// Package template compiles restricted SQL templates with named bound values.
package template

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Template is SQL text containing {{bind "name"}} actions.
type Template struct {
	name        string
	text        string
	parameters  []string
	uniqueNames []string
}

// Parse validates source and returns a restricted SQL template.
func Parse(name string, source string) (Template, error) {
	if name == "" {
		return Template{}, fmt.Errorf("template: name must not be empty")
	}
	if strings.TrimSpace(source) == "" {
		return Template{}, fmt.Errorf("template %q: source must not be empty", name)
	}

	var text strings.Builder
	parameters := make([]string, 0)
	uniqueNames := make([]string, 0)
	seen := make(map[string]struct{})
	remaining := source
	for remaining != "" {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			text.WriteString(remaining)
			break
		}
		text.WriteString(remaining[:start])
		remaining = remaining[start+2:]
		end := strings.Index(remaining, "}}")
		if end < 0 {
			return Template{}, fmt.Errorf("template %q: unclosed action", name)
		}
		parameter, err := parseBindAction(strings.TrimSpace(remaining[:end]))
		if err != nil {
			return Template{}, fmt.Errorf("template %q: %w", name, err)
		}
		text.WriteString(marker(len(parameters)))
		parameters = append(parameters, parameter)
		if _, exists := seen[parameter]; !exists {
			seen[parameter] = struct{}{}
			uniqueNames = append(uniqueNames, parameter)
		}
		remaining = remaining[end+2:]
	}
	return Template{name: name, text: text.String(), parameters: parameters, uniqueNames: uniqueNames}, nil
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

// Compile renders template placeholders for d.
func (t Template) Compile(d dialect.Dialect) (Compiled, error) {
	if d == nil {
		return Compiled{}, fmt.Errorf("template %q: dialect must not be nil", t.name)
	}
	if t.name == "" || t.text == "" {
		return Compiled{}, fmt.Errorf("template: invalid template")
	}
	placeholders := make([]string, len(t.parameters))
	for index := range t.parameters {
		placeholder, err := d.Placeholder(index + 1)
		if err != nil {
			return Compiled{}, fmt.Errorf("template %q: placeholder %d: %w", t.name, index+1, err)
		}
		placeholders[index] = placeholder
	}

	parts := scanMarkerParts(t.text)
	for index, placeholder := range placeholders {
		parts = replaceFirstMarker(parts, marker(index), placeholder)
	}
	var sql strings.Builder
	sql.Grow(len(t.text))
	for _, part := range parts {
		sql.WriteString(part.text)
	}
	return Compiled{
		name:        t.name,
		sql:         sql.String(),
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

// ParameterNames returns unique parameters in first-use order.
func (c Compiled) ParameterNames() []string {
	return append([]string(nil), c.uniqueNames...)
}

// Bind supplies all named values and returns a parameterized statement.
func (c Compiled) Bind(values map[string]any) (render.Statement, error) {
	if c.name == "" || c.sql == "" {
		return render.Statement{}, fmt.Errorf("template: invalid compiled template")
	}
	args := make([]any, len(c.parameters))
	for index, name := range c.parameters {
		value, ok := values[name]
		if !ok {
			return render.Statement{}, fmt.Errorf("template %q: missing value for %q", c.name, name)
		}
		args[index] = value
	}
	for name := range values {
		if !contains(c.uniqueNames, name) {
			return render.Statement{}, fmt.Errorf("template %q: unused value %q", c.name, name)
		}
	}
	statement, err := render.Precompiled(c.sql, args...)
	if err != nil {
		return render.Statement{}, fmt.Errorf("template %q: %w", c.name, err)
	}
	return statement, nil
}

// GoSource returns a Go function that creates this static statement.
func (c Compiled) GoSource(packageName string, functionName string) ([]byte, error) {
	if !isUsableGoIdentifier(packageName) {
		return nil, fmt.Errorf("template: invalid package name %q", packageName)
	}
	if !isUsableGoIdentifier(functionName) {
		return nil, fmt.Errorf("template %q: invalid function name %q", c.name, functionName)
	}
	if functionName == "init" || (packageName == "main" && functionName == "main") {
		return nil, fmt.Errorf("template %q: function name %q cannot be generated in package %q", c.name, functionName, packageName)
	}
	reservedNames := map[string]struct{}{
		packageName:  {},
		functionName: {},
	}
	for _, name := range c.uniqueNames {
		if !isUsableGoIdentifier(name) {
			return nil, fmt.Errorf("template %q: parameter %q cannot be a Go identifier", c.name, name)
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
		return nil, fmt.Errorf("template %q: format source: %w", c.name, err)
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

func marker(index int) string {
	return fmt.Sprintf("\x00rasql-bind-%d\x00", index)
}

type markerPart struct {
	text     string
	isMarker bool
}

func scanMarkerParts(text string) []markerPart {
	const markerPrefix = "\x00rasql-bind-"

	parts := make([]markerPart, 0)
	remaining := text
	for remaining != "" {
		start := strings.Index(remaining, markerPrefix)
		if start < 0 {
			parts = append(parts, markerPart{text: remaining})
			break
		}
		end := strings.IndexByte(remaining[start+len(markerPrefix):], '\x00')
		if end < 0 {
			parts = append(parts, markerPart{text: remaining})
			break
		}
		end += start + len(markerPrefix)

		index := remaining[start+len(markerPrefix) : end]
		if index == "" || strings.IndexFunc(index, func(r rune) bool {
			return r < '0' || r > '9'
		}) >= 0 {
			parts = append(parts, markerPart{text: remaining[:start+len(markerPrefix)]})
			remaining = remaining[start+len(markerPrefix):]
			continue
		}

		if start > 0 {
			parts = append(parts, markerPart{text: remaining[:start]})
		}
		parts = append(parts, markerPart{text: remaining[start : end+1], isMarker: true})
		remaining = remaining[end+1:]
	}
	return parts
}

func replaceFirstMarker(parts []markerPart, target string, replacement string) []markerPart {
	for index, part := range parts {
		if !part.isMarker || part.text != target {
			continue
		}
		replacementParts := scanMarkerParts(replacement)
		updated := make([]markerPart, 0, len(parts)-1+len(replacementParts))
		updated = append(updated, parts[:index]...)
		updated = append(updated, replacementParts...)
		updated = append(updated, parts[index+1:]...)
		return updated
	}
	return parts
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
