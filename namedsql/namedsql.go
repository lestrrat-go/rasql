// Package namedsql compiles SQL written with named bind actions into a
// statement carrying one dialect's placeholders, with the bound values in
// placeholder order. The only action it accepts is {{bind "name"}}, so a
// value can never become SQL text. A bind may also name the column it
// stands for, {{bind "name" table.column}}, which lets GoSource generate
// that column's Go type instead of any.
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
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Template is SQL text containing {{bind "name"}} actions, each optionally
// naming the column it stands for.
type Template struct {
	name        string
	parts       []templatePart
	parameters  []string
	uniqueNames []string
	columnRefs  []columnRef
}

// columnRef is the column a bind names, as written: "users.email" parses to
// {table: "users", column: "email"} and "audit.events.id" adds the schema.
// A zero columnRef means the bind named no column, which keeps its generated
// parameter typed any.
type columnRef struct {
	schemaName string
	table      string
	column     string
}

func (r columnRef) stated() bool { return r.column != "" }

// tableString returns the table part of the reference as written: "table"
// for an unqualified reference, "schema.table" for a qualified one.
func (r columnRef) tableString() string {
	if r.schemaName != "" {
		return r.schemaName + "." + r.table
	}
	return r.table
}

// String returns the reference as written, for error messages.
func (r columnRef) String() string {
	if r.schemaName != "" {
		return r.schemaName + "." + r.table + "." + r.column
	}
	return r.table + "." + r.column
}

// columnRefDescription describes ref for an error message: "no column" for
// an unstated reference, "column table.column" for a stated one.
func columnRefDescription(ref columnRef) string {
	if !ref.stated() {
		return "no column"
	}
	return "column " + ref.String()
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
	columnRefs := make([]columnRef, 0)
	seen := make(map[string]columnRef)
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
		parameter, ref, err := parseBindAction(strings.TrimSpace(remaining[:end]))
		if err != nil {
			return Template{}, fmt.Errorf("namedsql %q: %w", name, err)
		}
		bindIndex := len(parameters)
		parts = append(parts, templatePart{bindIndex: bindIndex, isBindPart: true})
		parameters = append(parameters, parameter)
		existing, exists := seen[parameter]
		switch {
		case !exists:
			seen[parameter] = ref
			uniqueNames = append(uniqueNames, parameter)
			columnRefs = append(columnRefs, ref)
		case existing == ref:
			// Repeated use naming the identical reference (or both untyped):
			// collapses to the one parameter already recorded.
		case existing.stated() && ref.stated():
			return Template{}, fmt.Errorf("namedsql %q: bind %q names column %s here and %s earlier; one parameter cannot stand for two columns", name, parameter, ref, existing)
		default:
			return Template{}, fmt.Errorf("namedsql %q: bind %q names %s here and %s earlier; every use of a name must name the same column or none", name, parameter, columnRefDescription(ref), columnRefDescription(existing))
		}
		remaining = remaining[end+2:]
	}
	return Template{name: name, parts: parts, parameters: parameters, uniqueNames: uniqueNames, columnRefs: columnRefs}, nil
}

// parseBindAction parses the trimmed text inside a {{ ... }} action. It
// accepts "bind "name"" (untyped) and "bind "name" table.column" or
// "bind "name" schema.table.column" (typed), returning the parameter name
// and the column reference it names, a zero columnRef for the untyped form.
func parseBindAction(action string) (string, columnRef, error) {
	fields := strings.Fields(action)
	if (len(fields) != 2 && len(fields) != 3) || fields[0] != "bind" {
		return "", columnRef{}, fmt.Errorf("actions must use bind with one quoted parameter name, optionally followed by a column reference")
	}
	name, err := strconv.Unquote(fields[1])
	if err != nil {
		return "", columnRef{}, fmt.Errorf("parameter name must be quoted")
	}
	if err := schema.ValidateIdentifier(name); err != nil {
		return "", columnRef{}, fmt.Errorf("invalid parameter name: %w", err)
	}
	if len(fields) == 2 {
		return name, columnRef{}, nil
	}
	ref, err := parseColumnRef(fields[2])
	if err != nil {
		return "", columnRef{}, err
	}
	return name, ref, nil
}

// parseColumnRef parses the unquoted table.column or schema.table.column
// reference that may follow a bind's parameter name.
func parseColumnRef(text string) (columnRef, error) {
	if strings.HasPrefix(text, `"`) {
		return columnRef{}, fmt.Errorf("column reference must not be quoted; write table.column, not a Go type name")
	}
	parts := strings.Split(text, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return columnRef{}, fmt.Errorf("invalid column reference %q: must be table.column or schema.table.column", text)
	}
	for _, part := range parts {
		if err := schema.ValidateIdentifier(part); err != nil {
			return columnRef{}, fmt.Errorf("invalid column reference %q: %w", text, err)
		}
	}
	if len(parts) == 2 {
		return columnRef{table: parts[0], column: parts[1]}, nil
	}
	return columnRef{schemaName: parts[0], table: parts[1], column: parts[2]}, nil
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
		columnRefs:  append([]columnRef(nil), t.columnRefs...),
	}, nil
}

// Compiled is a template with dialect-specific placeholders.
type Compiled struct {
	name        string
	sql         string
	parameters  []string
	uniqueNames []string
	columnRefs  []columnRef
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

// GoSource returns a Go function that creates this static statement. A bind
// that names no column generates an any parameter, as it always has. A bind
// that names a column resolves it against tables and generates that
// column's Go type instead; tables is optional and needed only when at
// least one bind names a column.
func (c Compiled) GoSource(packageName string, functionName string, tables ...schema.TableDef) ([]byte, error) {
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
	defaultParameterType := "any"
	if functionName == "any" {
		defaultParameterType = "interface{}"
	}
	errorType := "error"
	if functionName == "error" {
		errorType = "interface{ Error() string }"
	}

	parameterTypes := make([]string, len(c.uniqueNames))
	needsTime := false
	for index, ref := range c.columnRefs {
		if !ref.stated() {
			parameterTypes[index] = defaultParameterType
			continue
		}
		column, err := resolveColumn(c.uniqueNames[index], ref, tables)
		if err != nil {
			return nil, fmt.Errorf("namedsql %q: %w", c.name, err)
		}
		goType := schemagen.ColumnGoType(column)
		if goType == "time.Time" {
			needsTime = true
		}
		parameterTypes[index] = goType
	}

	timeName := "time"
	if needsTime {
		timeName = availableGoIdentifier("time", reservedNames)
		for index, parameterType := range parameterTypes {
			if parameterType == "time.Time" {
				parameterTypes[index] = timeName + ".Time"
			}
		}
	}

	var source bytes.Buffer
	source.WriteString("// Code generated by rasqlgen; DO NOT EDIT.\n\n")
	source.WriteString("package ")
	source.WriteString(packageName)
	source.WriteString("\n\n")
	if needsTime {
		source.WriteString("import (\n\t")
		source.WriteString(renderName)
		source.WriteString(" \"github.com/lestrrat-go/rasql/render\"\n\t")
		if timeName != "time" {
			source.WriteString(timeName)
			source.WriteByte(' ')
		}
		source.WriteString("\"time\"\n)\n\n")
	} else {
		source.WriteString("import ")
		source.WriteString(renderName)
		source.WriteString(" \"github.com/lestrrat-go/rasql/render\"\n\n")
	}
	source.WriteString("func ")
	source.WriteString(functionName)
	source.WriteByte('(')
	for index, name := range c.uniqueNames {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteString(name)
		source.WriteByte(' ')
		source.WriteString(parameterTypes[index])
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

// resolveColumn looks ref up among tables and returns the column it names.
// Matching is exact and case-sensitive on schema name, table name and
// column name: a descriptor's names come from inspection verbatim, and
// folding case would guess wrong on some engine.
//
// A two-part reference matches on table name alone, ignoring
// TableDef.Schema; two descriptors sharing that name is an error asking the
// writer to qualify it, the same rule applyHints already applies to hint
// keys. A three-part reference matches TableDef.Schema and TableDef.Name
// together. A ref that does not resolve is always an error: there is no
// fallback to any.
func resolveColumn(bindName string, ref columnRef, tables []schema.TableDef) (schema.ColumnDef, error) {
	if len(tables) == 0 {
		return schema.ColumnDef{}, fmt.Errorf("bind %q names %s, but no table descriptors were supplied", bindName, ref)
	}

	matches := make([]schema.TableDef, 0, 1)
	for _, table := range tables {
		if ref.schemaName != "" {
			if table.Schema == ref.schemaName && table.Name == ref.table {
				matches = append(matches, table)
			}
			continue
		}
		if table.Name == ref.table {
			matches = append(matches, table)
		}
	}

	switch len(matches) {
	case 0:
		return schema.ColumnDef{}, fmt.Errorf("bind %q names %s: no descriptor names table %s", bindName, ref, ref.tableString())
	case 1:
		column, ok := matches[0].Column(ref.column)
		if !ok {
			return schema.ColumnDef{}, fmt.Errorf("bind %q names %s: table %s has no column %s", bindName, ref, ref.tableString(), ref.column)
		}
		return column, nil
	default:
		return schema.ColumnDef{}, fmt.Errorf("bind %q names %s: two descriptors name table %s; qualify it as schema.table.column", bindName, ref, ref.tableString())
	}
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
