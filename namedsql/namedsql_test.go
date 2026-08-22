package namedsql_test

import (
	"fmt"
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

func TestBindRejectsInvalidCompiledTemplate(t *testing.T) {
	t.Run("blank placeholder", func(t *testing.T) {
		parsed, err := namedsql.Parse("user_by_id", "{{bind \"id\"}}")
		require.NoError(t, err)
		compiled, err := parsed.Compile(blankPlaceholderDialect{})
		require.NoError(t, err)

		_, err = compiled.Bind(map[string]any{"id": 1})
		require.EqualError(t, err, "namedsql: invalid compiled template")
	})

	t.Run("whitespace only sql", func(t *testing.T) {
		parsed, err := namedsql.Parse("whitespace_only", "{{bind \"id\"}}")
		require.NoError(t, err)
		compiled, err := parsed.Compile(whitespacePlaceholderDialect{})
		require.NoError(t, err)
		require.Equal(t, "   ", compiled.SQL())
		require.Equal(t, "   ", compiled.QueryDef().SQL)

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

// TestQueryDefCarriesEverythingAGeneratorReads pins the hand-off contract
// between namedsql and a code generator: QueryDef must carry exactly what
// GoSource used to read off Compiled directly, and the slices it returns
// must be copies so a caller cannot reach back into the Compiled.
func TestQueryDefCarriesEverythingAGeneratorReads(t *testing.T) {
	source := `SELECT id FROM widgets ` +
		`WHERE id = {{bind "id" widgets.id}} OR owner_id = {{bind "id" widgets.id}} ` +
		`OR name = {{bind "name"}} ` +
		`OR audit_id = {{bind "auditId" audit.events.id}}`
	parsed, err := namedsql.Parse("widget_query", source)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)

	def := compiled.QueryDef()
	require.Equal(t, "widget_query", def.Name)
	require.Equal(t, compiled.SQL(), def.SQL)
	require.Equal(t, []string{"id", "id", "name", "auditId"}, def.Parameters)
	require.Equal(t, []namedsql.BindDef{
		{Name: "id", Table: "widgets", Column: "id"},
		{Name: "name"},
		{Name: "auditId", Schema: "audit", Table: "events", Column: "id"},
	}, def.Binds)
	require.Len(t, def.Binds, len(slices.Collect(compiled.ParameterNames())))

	def.Parameters[0] = "mutated"
	def.Binds[0].Name = "mutated"
	again := compiled.QueryDef()
	require.Equal(t, "id", again.Parameters[0])
	require.Equal(t, "id", again.Binds[0].Name)
}
