// Package namedsql compiles SQL written with named bind actions into a
// statement carrying one dialect's placeholders, with the bound values in
// placeholder order. The only action it accepts is {{bind "name"}}, so a
// value can never become SQL text. A bind may also name the column it
// stands for, {{bind "name" table.column}}, which records the column a
// parameter stands for so a code generator can give it that column's Go
// type instead of any.
package namedsql

import (
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
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

// QueryDef describes a compiled query in the terms a code generator needs:
// the name to report in an error, the SQL to embed, the parameter list to
// pass through, and what each parameter stands for. It is a plain
// description with no behaviour, so a generator reads a stated shape
// rather than this package's internals.
type QueryDef struct {
	// Name is the name the query was parsed under.
	Name string
	// SQL is the compiled statement, carrying this dialect's placeholders.
	SQL string
	// Parameters names the parameter each placeholder takes, in placeholder
	// order. A name bound twice appears twice, because the generated call
	// passes one argument per placeholder.
	Parameters []string
	// Binds describes each distinct parameter once, in first-use order.
	Binds []BindDef
}

// BindDef is one distinct parameter of a query, and the column it names.
// Table and Column are empty when the bind named no column, which keeps the
// generated parameter typed any. Schema is empty unless the reference was
// written schema-qualified.
type BindDef struct {
	Name   string
	Schema string
	Table  string
	Column string
}

// QueryDef returns a description of this compiled query for a code
// generator. The returned slices are copies, so the caller cannot reach
// back into the Compiled.
func (c Compiled) QueryDef() QueryDef {
	// Parse appends to uniqueNames and columnRefs in the same branch, so
	// the two are the same length for every Compiled Compile produces.
	binds := make([]BindDef, len(c.uniqueNames))
	for index, name := range c.uniqueNames {
		ref := c.columnRefs[index]
		binds[index] = BindDef{Name: name, Schema: ref.schemaName, Table: ref.table, Column: ref.column}
	}
	return QueryDef{
		Name:       c.name,
		SQL:        c.sql,
		Parameters: append([]string(nil), c.parameters...),
		Binds:      binds,
	}
}

// Bind supplies all named values and returns a parameterized statement.
func (c Compiled) Bind(values map[string]any) (stmt.Statement, error) {
	if c.name == "" || strings.TrimSpace(c.sql) == "" {
		return stmt.Statement{}, fmt.Errorf("namedsql: invalid compiled template")
	}
	args := make([]any, len(c.parameters))
	for index, name := range c.parameters {
		value, ok := values[name]
		if !ok {
			return stmt.Statement{}, fmt.Errorf("namedsql %q: missing value for %q", c.name, name)
		}
		args[index] = value
	}
	for name := range values {
		if !contains(c.uniqueNames, name) {
			return stmt.Statement{}, fmt.Errorf("namedsql %q: unused value %q", c.name, name)
		}
	}
	return stmt.New(sqltext.Text(c.sql), args...), nil
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
