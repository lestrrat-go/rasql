package template_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/template"
	"github.com/stretchr/testify/require"
)

func TestTemplateCompilesAndBindsInPlaceholderOrder(t *testing.T) {
	parsed, err := template.Parse("user_by_email", "SELECT id FROM users WHERE email = {{bind \"email\"}} OR backup_email = {{ bind \"email\" }} AND active = {{bind \"active\"}}")
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
	parsed, err := template.Parse("marker_collision", literalMarker+"{{bind \"a\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, literalMarker+"$1", compiled.SQL())
}

func TestTemplateCompilePreservesLiteralMarkerBeforeBinds(t *testing.T) {
	const literalMarker = "\x00rasql-bind-1\x00"
	parsed, err := template.Parse("marker_collision", literalMarker+"{{bind \"a\"}}{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, literalMarker+"$1$2", compiled.SQL())
}

func TestTemplateCompilePreservesRepeatedLiteralMarker(t *testing.T) {
	const literalMarker = "\x00rasql-bind-0\x00"
	parsed, err := template.Parse("repeated_marker_collision", literalMarker+"{{bind \"a\"}}"+literalMarker+"{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, literalMarker+"$1"+literalMarker+"$2", compiled.SQL())
}

func TestTemplateCompilePreservesMalformedMarkerPrefix(t *testing.T) {
	const malformedMarkerPrefix = "\x00rasql-bind-junk"
	parsed, err := template.Parse("malformed_marker_prefix", malformedMarkerPrefix+"{{bind \"a\"}}")
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
			parsed, err := template.Parse("custom_dialect_marker", test.source)
			require.NoError(t, err)

			compiled, err := parsed.Compile(markerDialect{})
			require.NoError(t, err)
			require.Equal(t, test.expected, compiled.SQL())
		})
	}
}

func TestTemplateCompilePreservesMarkerReplacementOrderAcrossMarkerDialect(t *testing.T) {
	const literalMarker = "\x00rasql-bind-1\x00"
	parsed, err := template.Parse("cross_marker_collision", literalMarker+"{{bind \"a\"}}{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(crossMarkerDialect{})
	require.NoError(t, err)
	require.Equal(t, literalMarker+"p1"+"\x00rasql-bind-0\x00", compiled.SQL())
}

func TestTemplateCompileRendersManyMarkersWithBoundedAllocations(t *testing.T) {
	const bindCount = 128
	parsed, err := template.Parse("many_binds", strings.Repeat("{{bind \"value\"}}", bindCount))
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
	_, err := template.Parse("bad", "SELECT {{ .Value }}")
	require.Error(t, err)
	_, err = template.Parse("bad", "SELECT {{bind \"not-valid\"}}")
	require.Error(t, err)
}

func TestTemplateCompileRejectsNilPointerDialect(t *testing.T) {
	var nilDialect *nilPointerDialect

	t.Run("with bind action", func(t *testing.T) {
		parsed, err := template.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
		require.NoError(t, err)

		var compiled template.Compiled
		require.NotPanics(t, func() {
			compiled, err = parsed.Compile(nilDialect)
		})
		require.Zero(t, compiled)
		require.EqualError(t, err, `template "user_by_id": dialect must not be nil`)
	})

	t.Run("without bind action", func(t *testing.T) {
		parsed, err := template.Parse("select_one", "SELECT 1")
		require.NoError(t, err)

		var compiled template.Compiled
		require.NotPanics(t, func() {
			compiled, err = parsed.Compile(nilDialect)
		})
		require.Zero(t, compiled)
		require.EqualError(t, err, `template "select_one": dialect must not be nil`)
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
	parsed, err := template.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := compiled.GoSource("generated", "UserByID")
	require.NoError(t, err)
	requireGeneratedSourceCompiles(t, source)
}

func TestGoSourceCompilesWithCollidingGeneratedNames(t *testing.T) {
	parsed, err := template.Parse("user_by_values", "SELECT id FROM users WHERE first = {{bind \"render\"}} OR second = {{bind \"rasqlrender\"}} OR third = {{bind \"rasqlrender1\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := compiled.GoSource("generated", "rasqlrender2")
	require.NoError(t, err)
	requireGeneratedSourceCompiles(t, source)
}

func TestGoSourceCompilesWithPredeclaredFunctionNames(t *testing.T) {
	parsed, err := template.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
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
	parsed, err := template.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
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
	parsed, err := template.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"_\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = compiled.GoSource("generated", "UserByID")
	require.Error(t, err)
}

func TestGoSourceRejectsInvalidCompiledTemplate(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		var compiled template.Compiled
		source, err := compiled.GoSource("generated", "Query")
		require.Nil(t, source)
		require.EqualError(t, err, "template: invalid compiled template")
	})

	t.Run("blank placeholder", func(t *testing.T) {
		parsed, err := template.Parse("user_by_id", "{{bind \"id\"}}")
		require.NoError(t, err)
		compiled, err := parsed.Compile(blankPlaceholderDialect{})
		require.NoError(t, err)

		source, err := compiled.GoSource("generated", "Query")
		require.Nil(t, source)
		require.EqualError(t, err, "template: invalid compiled template")

		_, err = compiled.Bind(map[string]any{"id": 1})
		require.EqualError(t, err, "template: invalid compiled template")
	})

	t.Run("whitespace only sql", func(t *testing.T) {
		parsed, err := template.Parse("whitespace_only", "{{bind \"id\"}}")
		require.NoError(t, err)
		compiled, err := parsed.Compile(whitespacePlaceholderDialect{})
		require.NoError(t, err)
		require.Equal(t, "   ", compiled.SQL())

		source, err := compiled.GoSource("generated", "Query")
		require.Nil(t, source)
		require.EqualError(t, err, "template: invalid compiled template")

		_, err = compiled.Bind(map[string]any{"id": 1})
		require.EqualError(t, err, "template: invalid compiled template")
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
