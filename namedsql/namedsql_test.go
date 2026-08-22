package namedsql_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestTemplateCompilesAndBindsInPlaceholderOrder(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_email", "SELECT id FROM users WHERE email = {{bind \"email\"}} OR backup_email = {{ bind \"email\" }} AND active = {{bind \"active\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, "SELECT id FROM users WHERE email = $1 OR backup_email = $2 AND active = $3", compiled.SQL())
	require.Equal(t, []string{"email", "active"}, slices.Collect(compiled.ParameterNames()))

	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com", "active": true})
	require.NoError(t, err)
	require.Equal(t, []any{"ada@example.com", "ada@example.com", true}, statement.Args())
	require.NotContains(t, statement.SQL(), "ada@example.com")

	_, err = compiled.Bind(map[string]any{"email": "ada@example.com"})
	require.Error(t, err)
	_, err = compiled.Bind(map[string]any{"email": "ada@example.com", "active": true, "unused": 1})
	require.Error(t, err)
}

func TestTemplateCompilePreservesCompleteLiteralMarker(t *testing.T) {
	const literalMarker = "\x00rasql-bind-0\x00"
	parsed, err := namedsql.Parse("marker_collision", literalMarker+"{{bind \"a\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, literalMarker+"$1", compiled.SQL())
}

func TestTemplateCompilePreservesLiteralMarkerBeforeBinds(t *testing.T) {
	const literalMarker = "\x00rasql-bind-1\x00"
	parsed, err := namedsql.Parse("marker_collision", literalMarker+"{{bind \"a\"}}{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, literalMarker+"$1$2", compiled.SQL())
}

func TestTemplateCompilePreservesRepeatedLiteralMarker(t *testing.T) {
	const literalMarker = "\x00rasql-bind-0\x00"
	parsed, err := namedsql.Parse("repeated_marker_collision", literalMarker+"{{bind \"a\"}}"+literalMarker+"{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, literalMarker+"$1"+literalMarker+"$2", compiled.SQL())
}

func TestTemplateCompilePreservesMalformedMarkerPrefix(t *testing.T) {
	const malformedMarkerPrefix = "\x00rasql-bind-junk"
	parsed, err := namedsql.Parse("malformed_marker_prefix", malformedMarkerPrefix+"{{bind \"a\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, malformedMarkerPrefix+"$1", compiled.SQL())

	statement, err := compiled.Bind(map[string]any{"a": 1})
	require.NoError(t, err)
	require.Equal(t, malformedMarkerPrefix+"$1", statement.SQL())
	require.Equal(t, []any{1}, statement.Args())
}

func TestTemplateCompilePreservesCustomDialectMarkerPlaceholders(t *testing.T) {
	const (
		literalMarker         = "\x00rasql-bind-0\x00"
		marker                = "\x00rasql-bind-1\x00"
		malformedMarkerPrefix = "\x00rasql-bind-junk"
	)

	for _, test := range []struct {
		name     string
		source   string
		expected string
	}{
		{
			name:     "parser markers only",
			source:   "{{bind \"a\"}}{{bind \"b\"}}",
			expected: marker + "p2",
		},
		{
			name:     "complete literal marker",
			source:   literalMarker + "{{bind \"a\"}}",
			expected: literalMarker + marker,
		},
		{
			name:     "malformed marker prefix",
			source:   malformedMarkerPrefix + "{{bind \"a\"}}{{bind \"b\"}}",
			expected: malformedMarkerPrefix + marker + "p2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := namedsql.Parse("custom_dialect_marker", test.source)
			require.NoError(t, err)

			compiled, err := parsed.Compile(markerDialect{})
			require.NoError(t, err)
			require.Equal(t, test.expected, compiled.SQL())
		})
	}
}

func TestTemplateCompilePreservesMarkerReplacementOrderAcrossMarkerDialect(t *testing.T) {
	const literalMarker = "\x00rasql-bind-1\x00"
	parsed, err := namedsql.Parse("cross_marker_collision", literalMarker+"{{bind \"a\"}}{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(crossMarkerDialect{})
	require.NoError(t, err)
	require.Equal(t, literalMarker+"p1"+"\x00rasql-bind-0\x00", compiled.SQL())
}

func TestTemplateCompileRendersManyMarkersWithBoundedAllocations(t *testing.T) {
	const bindCount = 128
	parsed, err := namedsql.Parse("many_binds", strings.Repeat("{{bind \"value\"}}", bindCount))
	require.NoError(t, err)

	var compileErr error
	allocations := testing.AllocsPerRun(5, func() {
		_, compileErr = parsed.Compile(constantPlaceholderDialect{})
	})
	require.NoError(t, compileErr)
	require.Less(t, allocations, 16.0)

	compiled, err := parsed.Compile(constantPlaceholderDialect{})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("?", bindCount), compiled.SQL())
}

func TestTemplateRejectsUnrestrictedActions(t *testing.T) {
	_, err := namedsql.Parse("bad", "SELECT {{ .Value }}")
	require.Error(t, err)
	_, err = namedsql.Parse("bad", "SELECT {{bind \"not-valid\"}}")
	require.Error(t, err)
}

func TestTemplateCompileRejectsNilPointerDialect(t *testing.T) {
	var nilDialect *nilPointerDialect

	t.Run("with bind action", func(t *testing.T) {
		parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
		require.NoError(t, err)

		var compiled namedsql.Compiled
		require.NotPanics(t, func() {
			compiled, err = parsed.Compile(nilDialect)
		})
		require.Zero(t, compiled)
		require.EqualError(t, err, `namedsql "user_by_id": dialect must not be nil`)
	})

	t.Run("without bind action", func(t *testing.T) {
		parsed, err := namedsql.Parse("select_one", "SELECT 1")
		require.NoError(t, err)

		var compiled namedsql.Compiled
		require.NotPanics(t, func() {
			compiled, err = parsed.Compile(nilDialect)
		})
		require.Zero(t, compiled)
		require.EqualError(t, err, `namedsql "select_one": dialect must not be nil`)
	})
}

type nilPointerDialect struct {
	markerDialect
	prefix string
}

func (d *nilPointerDialect) Placeholder(position int) (string, error) {
	return fmt.Sprintf("%s%d", d.prefix, position), nil
}

type markerDialect struct{}

func (markerDialect) Name() string {
	return "marker"
}

func (markerDialect) QuoteIdentifier(name string) (string, error) {
	return name, nil
}

func (markerDialect) Placeholder(position int) (string, error) {
	if position == 1 {
		return "\x00rasql-bind-1\x00", nil
	}
	return "p2", nil
}

type crossMarkerDialect struct {
	markerDialect
}

func (crossMarkerDialect) Placeholder(position int) (string, error) {
	if position == 1 {
		return "p1", nil
	}
	return "\x00rasql-bind-0\x00", nil
}

type constantPlaceholderDialect struct {
	markerDialect
}

func (constantPlaceholderDialect) Placeholder(int) (string, error) {
	return "?", nil
}

func (markerDialect) TypeName(schema.ColumnDef) (string, error) {
	return "", nil
}

func (markerDialect) UpsertStyle() dialect.UpsertStyle {
	return dialect.UpsertUnsupported
}

func (markerDialect) Supports(dialect.Capability) bool {
	return false
}

func TestGoSourceCompiles(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := compiled.GoSource("generated", "UserByID")
	require.NoError(t, err)
	requireGeneratedSourceCompiles(t, source)
}

func TestGoSourceCompilesWithCollidingGeneratedNames(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_values", "SELECT id FROM users WHERE first = {{bind \"render\"}} OR second = {{bind \"rasqlrender\"}} OR third = {{bind \"rasqlrender1\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := compiled.GoSource("generated", "rasqlrender2")
	require.NoError(t, err)
	requireGeneratedSourceCompiles(t, source)
}

func TestGoSourceCompilesWithPredeclaredFunctionNames(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)

	for _, functionName := range []string{"any", "error"} {
		t.Run(functionName, func(t *testing.T) {
			source, err := compiled.GoSource("generated", functionName)
			require.NoError(t, err)
			requireGeneratedSourceCompiles(t, source)
		})
	}
}

func TestGoSourceRejectsNamesThatCannotCompile(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)

	for _, test := range []struct {
		name         string
		packageName  string
		functionName string
	}{
		{name: "blank package", packageName: "_", functionName: "UserByID"},
		{name: "blank function", packageName: "generated", functionName: "_"},
		{name: "init function", packageName: "generated", functionName: "init"},
		{name: "main entry point", packageName: "main", functionName: "main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := compiled.GoSource(test.packageName, test.functionName)
			require.Error(t, err)
		})
	}
}

func TestGoSourceRejectsBlankParameterName(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"_\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = compiled.GoSource("generated", "UserByID")
	require.Error(t, err)
}

func TestGoSourceRejectsInvalidCompiledTemplate(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		var compiled namedsql.Compiled
		source, err := compiled.GoSource("generated", "Query")
		require.Nil(t, source)
		require.EqualError(t, err, "namedsql: invalid compiled template")
	})

	t.Run("blank placeholder", func(t *testing.T) {
		parsed, err := namedsql.Parse("user_by_id", "{{bind \"id\"}}")
		require.NoError(t, err)
		compiled, err := parsed.Compile(blankPlaceholderDialect{})
		require.NoError(t, err)

		source, err := compiled.GoSource("generated", "Query")
		require.Nil(t, source)
		require.EqualError(t, err, "namedsql: invalid compiled template")

		_, err = compiled.Bind(map[string]any{"id": 1})
		require.EqualError(t, err, "namedsql: invalid compiled template")
	})

	t.Run("whitespace only sql", func(t *testing.T) {
		parsed, err := namedsql.Parse("whitespace_only", "{{bind \"id\"}}")
		require.NoError(t, err)
		compiled, err := parsed.Compile(whitespacePlaceholderDialect{})
		require.NoError(t, err)
		require.Equal(t, "   ", compiled.SQL())

		source, err := compiled.GoSource("generated", "Query")
		require.Nil(t, source)
		require.EqualError(t, err, "namedsql: invalid compiled template")

		_, err = compiled.Bind(map[string]any{"id": 1})
		require.EqualError(t, err, "namedsql: invalid compiled template")
	})
}

type blankPlaceholderDialect struct {
	markerDialect
}

func (blankPlaceholderDialect) Placeholder(int) (string, error) {
	return "", nil
}

type whitespacePlaceholderDialect struct {
	markerDialect
}

func (whitespacePlaceholderDialect) Placeholder(int) (string, error) {
	return "   ", nil
}

// TestParseAcceptsColumnReference covers the typed bind form the parser
// accepts: {{bind "name" table.column}} and its schema-qualified and
// extra-whitespace variants.
func TestParseAcceptsColumnReference(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "two-part reference", source: `SELECT id FROM users WHERE email = {{bind "email" users.email}}`},
		{name: "extra whitespace", source: `SELECT id FROM users WHERE email = {{ bind "email"  users.email }}`},
		{name: "schema-qualified reference", source: `SELECT id FROM audit.events WHERE id = {{bind "id" audit.events.id}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := namedsql.Parse("query", test.source)
			require.NoError(t, err)
		})
	}
}

// TestParseRejectsInvalidColumnReference covers every malformed form of the
// second bind argument the parser must refuse, each with its own reason.
func TestParseRejectsInvalidColumnReference(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "one-part reference", source: `SELECT 1 WHERE x = {{bind "email" email}}`},
		{name: "four-part reference", source: `SELECT 1 WHERE x = {{bind "id" a.b.c.d}}`},
		{name: "empty middle part", source: `SELECT 1 WHERE x = {{bind "id" users..email}}`},
		{name: "empty leading part", source: `SELECT 1 WHERE x = {{bind "id" .email}}`},
		{name: "empty trailing part", source: `SELECT 1 WHERE x = {{bind "id" users.}}`},
		{name: "invalid identifier part", source: `SELECT 1 WHERE x = {{bind "id" users.not-valid}}`},
		{name: "quoted second argument", source: `SELECT 1 WHERE x = {{bind "email" "users.email"}}`},
		{name: "four-field action", source: `SELECT 1 WHERE x = {{bind "email" users.email extra}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := namedsql.Parse("query", test.source)
			require.Error(t, err)
		})
	}
}

// TestParseRejectsMismatchedColumnReferences covers every way two uses of
// the same bind name can disagree: naming two different columns, and mixing
// a typed use with an untyped one, in both orders.
func TestParseRejectsMismatchedColumnReferences(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "two different columns", source: `SELECT 1 WHERE a = {{bind "id" users.id}} OR b = {{bind "id" orders.id}}`},
		{name: "typed then untyped", source: `SELECT 1 WHERE a = {{bind "id" users.id}} OR b = {{bind "id"}}`},
		{name: "untyped then typed", source: `SELECT 1 WHERE a = {{bind "id"}} OR b = {{bind "id" users.id}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := namedsql.Parse("query", test.source)
			require.Error(t, err)
		})
	}
}

// TestParseAcceptsRepeatedIdenticalColumnReference confirms a name reused
// with the identical reference collapses to one parameter, the same as a
// repeated untyped bind does today.
func TestParseAcceptsRepeatedIdenticalColumnReference(t *testing.T) {
	parsed, err := namedsql.Parse("query", `SELECT 1 WHERE a = {{bind "id" users.id}} OR b = {{bind "id" users.id}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, slices.Collect(compiled.ParameterNames()))
}

// TestCompileTypingDoesNotTouchRendering confirms that naming a column
// changes only the generated Go signature, never the rendered SQL or the
// parameter names Compile reports.
func TestCompileTypingDoesNotTouchRendering(t *testing.T) {
	untyped, err := namedsql.Parse("query", `SELECT id, email FROM users WHERE email = {{bind "email"}}`)
	require.NoError(t, err)
	typed, err := namedsql.Parse("query", `SELECT id, email FROM users WHERE email = {{bind "email" users.email}}`)
	require.NoError(t, err)

	compiledUntyped, err := untyped.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	compiledTyped, err := typed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)

	require.Equal(t, compiledUntyped.SQL(), compiledTyped.SQL())
	require.Equal(t, slices.Collect(compiledUntyped.ParameterNames()), slices.Collect(compiledTyped.ParameterNames()))
}

// TestGoSourceUntypedBindGoldenBytes pins the exact emitted bytes of an
// untyped bind. This is the guard for the additive claim: a bind with no
// column reference must keep generating exactly this, byte for byte, with
// or without the typed-parameter feature.
func TestGoSourceUntypedBindGoldenBytes(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_email", `SELECT id, email FROM users WHERE email = {{bind "email"}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := compiled.GoSource("generated", "UserByEmail")
	require.NoError(t, err)

	const want = "// Code generated by rasqlgen; DO NOT EDIT.\n\n" +
		"package generated\n\n" +
		"import rasqlrender \"github.com/lestrrat-go/rasql/render\"\n\n" +
		"func UserByEmail(email any) (rasqlrender.Statement, error) {\n" +
		"\treturn rasqlrender.Precompiled(\"SELECT id, email FROM users WHERE email = $1\", email)\n" +
		"}\n"
	require.Equal(t, want, string(source))
}

// widgetsTableDef carries one column of every schema type ColumnGoType maps,
// plus an unsigned-integer column, for the GoSource type-mapping tests.
func widgetsTableDef() schema.TableDef {
	return schema.TableDef{
		Name: "widgets",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "owner_id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "active", Type: schema.BooleanType{}},
			{Name: "weight", Type: schema.FloatType{}},
			{Name: "name", Type: schema.TextType{}},
			{Name: "external_id", Type: schema.UUIDType{}},
			{Name: "payload", Type: schema.BytesType{}},
			{Name: "attributes", Type: schema.JSONType{}},
			{Name: "created_at", Type: schema.TimeType{}},
			{Name: "price", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2)}},
		},
		PrimaryKey: []string{"id"},
	}
}

func TestGoSourceTypedBindEmitsColumnType(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
		want   string
	}{
		{name: "integer", column: "id", want: "int64"},
		{name: "unsigned integer", column: "owner_id", want: "uint64"},
		{name: "boolean", column: "active", want: "bool"},
		{name: "float", column: "weight", want: "float64"},
		{name: "text", column: "name", want: "string"},
		{name: "uuid", column: "external_id", want: "string"},
		{name: "bytes", column: "payload", want: "[]byte"},
		{name: "json", column: "attributes", want: "[]byte"},
		{name: "decimal", column: "price", want: "string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := `SELECT ` + test.column + ` FROM widgets WHERE ` + test.column + ` = {{bind "value" widgets.` + test.column + `}}`
			parsed, err := namedsql.Parse("widget_query", source)
			require.NoError(t, err)
			compiled, err := parsed.Compile(dialect.PostgreSQL())
			require.NoError(t, err)
			generated, err := compiled.GoSource("generated", "WidgetQuery", widgetsTableDef())
			require.NoError(t, err)
			require.Contains(t, string(generated), "func WidgetQuery(value "+test.want+")")
			requireGeneratedSourceCompiles(t, generated)
		})
	}
}

func TestGoSourceTypedBindEmitsTimeImport(t *testing.T) {
	parsed, err := namedsql.Parse("widget_by_created_at", `SELECT id FROM widgets WHERE created_at = {{bind "createdAt" widgets.created_at}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	generated, err := compiled.GoSource("generated", "WidgetByCreatedAt", widgetsTableDef())
	require.NoError(t, err)
	require.Contains(t, string(generated), `"time"`)
	require.Contains(t, string(generated), "func WidgetByCreatedAt(createdAt time.Time)")
	requireGeneratedSourceCompiles(t, generated)
}

// TestGoSourceTypedBindWithCollidingTimeName confirms a time-typed parameter
// in a package or function literally named "time" still compiles, by
// picking a non-colliding import identifier the same way GoSource already
// does for the rasqlrender import.
func TestGoSourceTypedBindWithCollidingTimeName(t *testing.T) {
	t.Run("package named time", func(t *testing.T) {
		parsed, err := namedsql.Parse("widget_by_created_at", `SELECT id FROM widgets WHERE created_at = {{bind "createdAt" widgets.created_at}}`)
		require.NoError(t, err)
		compiled, err := parsed.Compile(dialect.PostgreSQL())
		require.NoError(t, err)
		generated, err := compiled.GoSource("time", "WidgetByCreatedAt", widgetsTableDef())
		require.NoError(t, err)
		require.Contains(t, string(generated), `time1 "time"`)
	})

	t.Run("function named time", func(t *testing.T) {
		parsed, err := namedsql.Parse("widget_by_created_at", `SELECT id FROM widgets WHERE created_at = {{bind "createdAt" widgets.created_at}}`)
		require.NoError(t, err)
		compiled, err := parsed.Compile(dialect.PostgreSQL())
		require.NoError(t, err)
		generated, err := compiled.GoSource("generated", "time", widgetsTableDef())
		require.NoError(t, err)
		require.Contains(t, string(generated), `time1 "time"`)
		requireGeneratedSourceCompiles(t, generated)
	})
}

func TestGoSourceRepeatedTypedBindEmitsOneParameterPassedTwice(t *testing.T) {
	parsed, err := namedsql.Parse("widget_query", `SELECT id FROM widgets WHERE id = {{bind "id" widgets.id}} OR owner_id = {{bind "id" widgets.id}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	generated, err := compiled.GoSource("generated", "WidgetQuery", widgetsTableDef())
	require.NoError(t, err)
	require.Contains(t, string(generated), "func WidgetQuery(id int64)")
	require.Contains(t, string(generated), "Precompiled(\"SELECT id FROM widgets WHERE id = $1 OR owner_id = $2\", id, id)")
	requireGeneratedSourceCompiles(t, generated)
}

func TestGoSourceTypedBindErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		tables  []schema.TableDef
		wantErr string
	}{
		{
			name:    "no tables supplied",
			source:  `SELECT id FROM widgets WHERE id = {{bind "id" widgets.id}}`,
			tables:  nil,
			wantErr: `no table descriptors were supplied`,
		},
		{
			name:    "table absent",
			source:  `SELECT id FROM orders WHERE id = {{bind "id" orders.id}}`,
			tables:  []schema.TableDef{widgetsTableDef()},
			wantErr: `no descriptor names table orders`,
		},
		{
			name:    "column absent",
			source:  `SELECT id FROM widgets WHERE id = {{bind "id" widgets.missing}}`,
			tables:  []schema.TableDef{widgetsTableDef()},
			wantErr: `table widgets has no column missing`,
		},
		{
			name:   "unqualified name matches two descriptors in different schemas",
			source: `SELECT id FROM widgets WHERE id = {{bind "id" widgets.id}}`,
			tables: []schema.TableDef{
				{Name: "widgets", Schema: "north", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}},
				{Name: "widgets", Schema: "south", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}},
			},
			wantErr: `two descriptors name table widgets; qualify it as schema.table.column`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := namedsql.Parse("widget_query", test.source)
			require.NoError(t, err)
			compiled, err := parsed.Compile(dialect.PostgreSQL())
			require.NoError(t, err)
			_, err = compiled.GoSource("generated", "WidgetQuery", test.tables...)
			require.ErrorContains(t, err, test.wantErr)
			require.ErrorContains(t, err, `bind "id"`)
		})
	}
}

// TestGoSourceResolvesSchemaQualifiedReference confirms a three-part
// reference matches TableDef.Schema and TableDef.Name together, and does
// not match a same-named table carrying a different schema.
func TestGoSourceResolvesSchemaQualifiedReference(t *testing.T) {
	tables := []schema.TableDef{
		{Name: "events", Schema: "audit", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}},
		{Name: "events", Schema: "billing", Columns: []schema.ColumnDef{{Name: "id", Type: schema.TextType{}}}},
	}

	parsed, err := namedsql.Parse("event_by_id", `SELECT id FROM audit.events WHERE id = {{bind "id" audit.events.id}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	generated, err := compiled.GoSource("generated", "EventByID", tables...)
	require.NoError(t, err)
	require.Contains(t, string(generated), "func EventByID(id int64)")
}

// TestGoSourceIsDeterministic confirms calling GoSource twice with the same
// inputs returns identical bytes.
func TestGoSourceIsDeterministic(t *testing.T) {
	parsed, err := namedsql.Parse("widget_query", `SELECT id FROM widgets WHERE id = {{bind "id" widgets.id}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)

	first, err := compiled.GoSource("generated", "WidgetQuery", widgetsTableDef())
	require.NoError(t, err)
	second, err := compiled.GoSource("generated", "WidgetQuery", widgetsTableDef())
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func requireGeneratedSourceCompiles(t *testing.T, source []byte) {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "query.go"), source, 0o600))
	repoGoMod, err := os.ReadFile("../go.mod")
	require.NoError(t, err)
	// The replace directive must be absolute: t.TempDir() no longer nests the
	// scratch module under this package directory, so a relative ".." would
	// resolve against the temp directory's own location instead of the repo.
	repository, err := filepath.Abs("..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/generated\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	command := exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}
