package template_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
	require.Equal(t, []string{"email", "active"}, compiled.ParameterNames())

	statement, err := compiled.Bind(map[string]any{"email": "ada@example.com", "active": true})
	require.NoError(t, err)
	require.Equal(t, []any{"ada@example.com", "ada@example.com", true}, statement.Args())
	require.NotContains(t, statement.SQL(), "ada@example.com")

	_, err = compiled.Bind(map[string]any{"email": "ada@example.com"})
	require.Error(t, err)
	_, err = compiled.Bind(map[string]any{"email": "ada@example.com", "active": true, "unused": 1})
	require.Error(t, err)
}

func TestTemplateCompileReplacesFirstMarkerAcrossWholeText(t *testing.T) {
	const literalMarker = "\x00rasql-bind-1\x00"
	parsed, err := template.Parse("marker_collision", literalMarker+"{{bind \"a\"}}{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, "$2$1"+literalMarker, compiled.SQL())
}

func TestTemplateCompilePreservesRepeatedLiteralMarker(t *testing.T) {
	const literalMarker = "\x00rasql-bind-0\x00"
	parsed, err := template.Parse("repeated_marker_collision", literalMarker+"{{bind \"a\"}}"+literalMarker+"{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, "$1"+literalMarker+literalMarker+"$2", compiled.SQL())
}

func TestTemplateCompilePreservesCustomDialectMarkerPlaceholders(t *testing.T) {
	const marker = "\x00rasql-bind-1\x00"
	parsed, err := template.Parse("custom_dialect_marker", "{{bind \"a\"}}{{bind \"b\"}}")
	require.NoError(t, err)

	compiled, err := parsed.Compile(markerDialect{})
	require.NoError(t, err)
	require.Equal(t, "p2"+marker, compiled.SQL())
}

func TestTemplateRejectsUnrestrictedActions(t *testing.T) {
	_, err := template.Parse("bad", "SELECT {{ .Value }}")
	require.Error(t, err)
	_, err = template.Parse("bad", "SELECT {{bind \"not-valid\"}}")
	require.Error(t, err)
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

func (markerDialect) TypeName(schema.LogicalType) (string, error) {
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

func requireGeneratedSourceCompiles(t *testing.T, source []byte) {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".tmp-template-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	require.NoError(t, os.WriteFile(filepath.Join(directory, "query.go"), source, 0o600))
	module := "module example.com/generated\n\ngo 1.26.0\n\nrequire (\n\tgithub.com/lestrrat-go/rasql v0.0.0\n\tgithub.com/lestrrat-go/rasql-mysql v0.0.0-20260803090041-496b40acb82a\n\tgithub.com/lestrrat-go/rasql-pg v0.0.0-20260803045404-7e3faf0c19bd\n\tgithub.com/lestrrat-go/rasql-sqlite v0.0.0-20260803093924-08502c4f05f3\n)\n\nreplace github.com/lestrrat-go/rasql => ../..\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	command := exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}
