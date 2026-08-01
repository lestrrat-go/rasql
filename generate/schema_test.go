package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// generatedUsageTest exercises the emitted column fields and As method from
// inside the temporary module, so a defect in the generated source fails here
// rather than in a consumer.
const generatedUsageTest = `package generated_test

import (
	"testing"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

func TestGeneratedColumnFields(t *testing.T) {
	users := generated.Users()
	require.Equal(t, "id", users.ID.Name())
	require.Equal(t, "email", users.Email.Name())
	require.Equal(t, "created_at", users.CreatedAt.Name())
	require.Equal(t, "users", users.ID.Source().Qualifier())
	require.Equal(t, "users", users.QueryTable().Name())
}

func TestGeneratedAsRebindsColumns(t *testing.T) {
	users := generated.Users()
	manager, err := users.As("manager")
	require.NoError(t, err)
	require.Equal(t, "manager", manager.QueryTable().Qualifier())
	require.Equal(t, "manager", manager.ID.Source().Qualifier())
	require.Equal(t, "manager", manager.Email.Source().Qualifier())

	_, err = users.As("not an identifier")
	require.Error(t, err)
}

func TestGeneratedSelfJoinRendersAlias(t *testing.T) {
	users := generated.Users()
	manager, err := users.As("manager")
	require.NoError(t, err)

	statement, err := query.NewSelect(users.QueryTable(), query.Project(users.ID))
	require.NoError(t, err)
	statement, err = statement.WithJoin(rasql.InnerJoin(manager, query.Equal(users.ID, manager.ID)))
	require.NoError(t, err)

	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(
		t,
		` + "`" + `SELECT "users"."id" FROM "users" INNER JOIN "users" AS "manager" ON ("users"."id" = "manager"."id")` + "`" + `,
		rendered.SQL(),
	)
}
`

func TestSchemaIsDeterministicAndCompiles(t *testing.T) {
	users := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText, Nullable: true},
			{Name: "created_at", Type: schema.TypeTime},
		},
		PrimaryKey: []string{"id"},
	}
	orders := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.Schema("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "type OrdersRow struct")
	require.Contains(t, string(source), "type UsersRow struct")
	require.Contains(t, string(source), "Email")
	require.Contains(t, string(source), "CreatedAt")
	require.Contains(t, string(source), "`rasql:\"email\"`")
	require.Contains(t, string(source), "`rasql:\"created_at\"`")
	require.Contains(t, string(source), "\"github.com/lestrrat-go/rasql/query\"")
	require.Contains(t, string(source), "type UsersTable struct {\n\trasql.Table[UsersRow]\n")
	require.Contains(t, string(source), "\tCreatedAt query.Column\n")
	require.Contains(t, string(source), "func newUsersTable(table rasql.Table[UsersRow]) UsersTable {")
	require.Contains(t, string(source), "CreatedAt: rasql.MustColumn(table, \"created_at\"),")
	require.Contains(t, string(source), "var ordersTable = newOrdersTable(rasql.MustTable[OrdersRow](schema.Table{")
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTable[UsersRow](schema.Table{")
	require.Contains(t, string(source), "func Orders() OrdersTable {")
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.Contains(t, string(source), "func (t UsersTable) As(alias string) (UsersTable, error) {")
	require.NotContains(t, string(source), "var Users =")
	require.Less(t, stringIndex(t, source, "var ordersTable"), stringIndex(t, source, "var usersTable"))

	repeated, err := generate.Schema("generated", orders, users)
	require.NoError(t, err)
	require.Equal(t, string(source), string(repeated), "generated source must not depend on input order")

	directory, err := os.MkdirTemp(".", ".tmp-schema-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(directory))
	})
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(generatedUsageTest), 0o600))
	module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => ../..\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}

func TestSchemaRejectsInvalidPackageName(t *testing.T) {
	_, err := generate.Schema("not-valid")
	require.Error(t, err)
}

func TestSchemaRejectsReservedColumnFieldName(t *testing.T) {
	for _, columnName := range []string{"table", "as", "query_table", "column"} {
		t.Run(columnName, func(t *testing.T) {
			_, err := generate.Schema("generated", schema.Table{
				Name: "users",
				Columns: []schema.Column{
					{Name: "id", Type: schema.TypeInteger},
					{Name: columnName, Type: schema.TypeText},
				},
				PrimaryKey: []string{"id"},
			})
			require.ErrorContains(t, err, "reserved generated field")
			require.ErrorContains(t, err, columnName)
			require.ErrorContains(t, err, "users")
		})
	}
}

func TestSchemaRejectsCollidingGeneratedNames(t *testing.T) {
	_, err := generate.Schema("generated",
		schema.Table{
			Name:       "users",
			Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
			PrimaryKey: []string{"id"},
		},
		schema.Table{
			Name:       "users_table",
			Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
			PrimaryKey: []string{"id"},
		},
	)
	require.ErrorContains(t, err, "duplicates generated name")
	require.ErrorContains(t, err, "UsersTable")
}

func stringIndex(t *testing.T, source []byte, value string) int {
	t.Helper()
	index := len(source)
	for offset := 0; offset+len(value) <= len(source); offset++ {
		if string(source[offset:offset+len(value)]) == value {
			index = offset
			break
		}
	}
	require.NotEqual(t, len(source), index)
	return index
}
