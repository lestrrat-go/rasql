package querygen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestGoSourceCompiles(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := querygen.GoSource(compiled.QueryDef(), "generated", "UserByID")
	require.NoError(t, err)
	requireGeneratedSourceCompiles(t, source)
}

func TestGoSourceCompilesWithCollidingGeneratedNames(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_values", "SELECT id FROM users WHERE first = {{bind \"stmt\"}} OR second = {{bind \"stmt1\"}} OR third = {{bind \"stmt2\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := querygen.GoSource(compiled.QueryDef(), "generated", "stmt3")
	require.NoError(t, err)
	requireGeneratedSourceCompiles(t, source)
}

// TestGoSourceStmtImportCollisionFallsBackToAlias confirms that when the bare
// identifier "stmt" is already taken by a bind name, the function name, or
// the package name, GoSource falls back to an explicit alias built the same
// way it always has, rather than emitting uncompilable Go.
func TestGoSourceStmtImportCollisionFallsBackToAlias(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_values", "SELECT id FROM users WHERE first = {{bind \"stmt\"}} OR second = {{bind \"stmt1\"}} OR third = {{bind \"stmt2\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	source, err := querygen.GoSource(compiled.QueryDef(), "generated", "stmt3")
	require.NoError(t, err)

	require.Contains(t, string(source), `stmt4 "github.com/lestrrat-go/rasql/stmt"`)
	require.Contains(t, string(source), "stmt4.Statement")
	require.Contains(t, string(source), "stmt4.New(")
	requireGeneratedSourceCompiles(t, source)
}

func TestGoSourceCompilesWithPredeclaredFunctionNames(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"id\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)

	for _, functionName := range []string{"any", "error"} {
		t.Run(functionName, func(t *testing.T) {
			source, err := querygen.GoSource(compiled.QueryDef(), "generated", functionName)
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
			_, err := querygen.GoSource(compiled.QueryDef(), test.packageName, test.functionName)
			require.Error(t, err)
		})
	}
}

func TestGoSourceRejectsBlankParameterName(t *testing.T) {
	parsed, err := namedsql.Parse("user_by_id", "SELECT id FROM users WHERE id = {{bind \"_\"}}")
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = querygen.GoSource(compiled.QueryDef(), "generated", "UserByID")
	require.Error(t, err)
}

// TestGoSourceRejectsInvalidQueryDef confirms GoSource guards a QueryDef
// that carries no name or blank SQL, built directly as a literal since
// querygen takes a QueryDef and needs no dialect at all.
func TestGoSourceRejectsInvalidQueryDef(t *testing.T) {
	for _, test := range []struct {
		name string
		def  namedsql.QueryDef
	}{
		{name: "zero value", def: namedsql.QueryDef{}},
		{name: "blank sql", def: namedsql.QueryDef{Name: "user_by_id"}},
		{name: "whitespace only sql", def: namedsql.QueryDef{Name: "user_by_id", SQL: "   "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := querygen.GoSource(test.def, "generated", "Query")
			require.Nil(t, source)
			require.EqualError(t, err, "namedsql: invalid compiled template")
		})
	}
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
	source, err := querygen.GoSource(compiled.QueryDef(), "generated", "UserByEmail")
	require.NoError(t, err)

	const want = "// Code generated by rasqlgen; DO NOT EDIT.\n\n" +
		"package generated\n\n" +
		"import \"github.com/lestrrat-go/rasql/stmt\"\n\n" +
		"func UserByEmail(email any) stmt.Statement {\n" +
		"\treturn stmt.New(\"SELECT id, email FROM users WHERE email = $1\", email)\n" +
		"}\n"
	require.Equal(t, want, string(source))
}

// TestGoSourceTypedBindGoldenBytes pins the exact emitted bytes of the case
// that exercises the most emitter branches: a time.Time bind in a package
// named "time", which forces both the grouped import block and the "time1"
// rename. TestGoSourceUntypedBindGoldenBytes covers only the untyped case;
// every typed case elsewhere is asserted with require.Contains, which would
// pass even if the import block, spacing, or return signature changed.
func TestGoSourceTypedBindGoldenBytes(t *testing.T) {
	parsed, err := namedsql.Parse("widget_by_created_at", `SELECT id FROM widgets WHERE created_at = {{bind "createdAt" widgets.created_at}}`)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	generated, err := querygen.GoSource(compiled.QueryDef(), "time", "WidgetByCreatedAt", widgetsTableDef())
	require.NoError(t, err)

	const want = "// Code generated by rasqlgen; DO NOT EDIT.\n\n" +
		"package time\n\n" +
		"import (\n" +
		"\ttime1 \"time\"\n\n" +
		"\t\"github.com/lestrrat-go/rasql/stmt\"\n" +
		")\n\n" +
		"func WidgetByCreatedAt(createdAt time1.Time) stmt.Statement {\n" +
		"\treturn stmt.New(\"SELECT id FROM widgets WHERE created_at = $1\", createdAt)\n" +
		"}\n"
	require.Equal(t, want, string(generated))
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
			generated, err := querygen.GoSource(compiled.QueryDef(), "generated", "WidgetQuery", widgetsTableDef())
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
	generated, err := querygen.GoSource(compiled.QueryDef(), "generated", "WidgetByCreatedAt", widgetsTableDef())
	require.NoError(t, err)
	require.Contains(t, string(generated), `"time"`)
	require.Contains(t, string(generated), "func WidgetByCreatedAt(createdAt time.Time)")
	requireGeneratedSourceCompiles(t, generated)
}

// TestGoSourceTypedBindWithCollidingTimeName confirms a time-typed parameter
// in a package or function literally named "time" still compiles, by
// picking a non-colliding import identifier the same way GoSource already
// does for the stmt import.
func TestGoSourceTypedBindWithCollidingTimeName(t *testing.T) {
	t.Run("package named time", func(t *testing.T) {
		parsed, err := namedsql.Parse("widget_by_created_at", `SELECT id FROM widgets WHERE created_at = {{bind "createdAt" widgets.created_at}}`)
		require.NoError(t, err)
		compiled, err := parsed.Compile(dialect.PostgreSQL())
		require.NoError(t, err)
		generated, err := querygen.GoSource(compiled.QueryDef(), "time", "WidgetByCreatedAt", widgetsTableDef())
		require.NoError(t, err)
		require.Contains(t, string(generated), `time1 "time"`)
	})

	t.Run("function named time", func(t *testing.T) {
		parsed, err := namedsql.Parse("widget_by_created_at", `SELECT id FROM widgets WHERE created_at = {{bind "createdAt" widgets.created_at}}`)
		require.NoError(t, err)
		compiled, err := parsed.Compile(dialect.PostgreSQL())
		require.NoError(t, err)
		generated, err := querygen.GoSource(compiled.QueryDef(), "generated", "time", widgetsTableDef())
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
	generated, err := querygen.GoSource(compiled.QueryDef(), "generated", "WidgetQuery", widgetsTableDef())
	require.NoError(t, err)
	require.Contains(t, string(generated), "func WidgetQuery(id int64)")
	require.Contains(t, string(generated), "New(\"SELECT id FROM widgets WHERE id = $1 OR owner_id = $2\", id, id)")
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
			_, err = querygen.GoSource(compiled.QueryDef(), "generated", "WidgetQuery", test.tables...)
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
	generated, err := querygen.GoSource(compiled.QueryDef(), "generated", "EventByID", tables...)
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

	first, err := querygen.GoSource(compiled.QueryDef(), "generated", "WidgetQuery", widgetsTableDef())
	require.NoError(t, err)
	second, err := querygen.GoSource(compiled.QueryDef(), "generated", "WidgetQuery", widgetsTableDef())
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func requireGeneratedSourceCompiles(t *testing.T, source []byte) {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "query.go"), source, 0o600))
	repoGoMod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	// The replace directive must be absolute: t.TempDir() no longer nests the
	// scratch module under this package directory, so a relative ".." would
	// resolve against the temp directory's own location instead of the repo.
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	module := strings.Replace(string(repoGoMod), "module github.com/lestrrat-go/rasql\n", "module example.com/generated\n", 1)
	module += "\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	command := exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}
