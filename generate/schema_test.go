package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"time"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
	"github.com/stretchr/testify/require"
)

func TestGeneratedRowMapsItsOwnColumns(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := row.New(
		[]string{"id", "email", "created_at"},
		[]any{int64(7), "ada@example.com", createdAt},
	)
	require.NoError(t, err)

	// row.Decode finds DecodeRow on *UsersRow, so no tag is read.
	decoded, err := row.Decode[generated.UsersRow](result)
	require.NoError(t, err)
	require.Equal(t, int64(7), decoded.ID)
	require.NotNil(t, decoded.Email)
	require.Equal(t, "ada@example.com", *decoded.Email)
	require.Equal(t, createdAt, decoded.CreatedAt)

	// A nullable column decodes into a nil pointer rather than failing.
	nullEmail, err := row.New(
		[]string{"id", "email", "created_at"},
		[]any{int64(7), nil, createdAt},
	)
	require.NoError(t, err)
	decoded, err = row.Decode[generated.UsersRow](nullEmail)
	require.NoError(t, err)
	require.Nil(t, decoded.Email)

	// A missing column is reported by the generated DecodeRow.
	partial, err := row.New([]string{"id"}, []any{int64(7)})
	require.NoError(t, err)
	_, err = row.Decode[generated.UsersRow](partial)
	require.ErrorContains(t, err, ` + "`" + `column "email" is not present` + "`" + `)
}

func TestGeneratedRowSuppliesItsOwnColumnValues(t *testing.T) {
	var valuer rasql.ColumnValuer = generated.UsersRow{ID: 7}
	value, ok := valuer.ColumnValue("id")
	require.True(t, ok)
	require.Equal(t, int64(7), value)
	_, ok = valuer.ColumnValue("nickname")
	require.False(t, ok)
}

func TestGeneratedRowScansDirectly(t *testing.T) {
	var scanner row.Scanner = &generated.UsersRow{}

	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	err := scanner.ScanRow(scanSource{id: 7, email: "ada@example.com", createdAt: createdAt})
	require.NoError(t, err)
	decoded := scanner.(*generated.UsersRow)
	require.Equal(t, int64(7), decoded.ID)
	require.NotNil(t, decoded.Email)
	require.Equal(t, "ada@example.com", *decoded.Email)
	require.Equal(t, createdAt, decoded.CreatedAt)
}

func TestGeneratedRowScansTextTimes(t *testing.T) {
	for _, value := range []any{
		"2026-08-01 12:30:00",
		[]byte("2026-08-01 12:30:00"),
	} {
		row := &generated.UsersRow{}
		require.NoError(t, row.ScanRow(scanSource{id: 7, email: "ada@example.com", createdAt: value}))
		require.Equal(t, time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC), row.CreatedAt)

		destinations, err := row.ScanDestinations([]string{"created_at"})
		require.NoError(t, err)
		require.NoError(t, destinations[0].(interface{ Scan(any) error }).Scan(value))
		require.Equal(t, time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC), row.CreatedAt)
	}
}

func TestGeneratedRowMapsResultColumns(t *testing.T) {
	var decoded generated.UsersRow
	destinations, err := decoded.ScanDestinations([]string{"created_at", "id"})
	require.NoError(t, err)

	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	require.NoError(t, destinations[0].(interface{ Scan(any) error }).Scan(createdAt))
	*destinations[1].(*int64) = 7
	require.Equal(t, int64(7), decoded.ID)
	require.Nil(t, decoded.Email)
	require.Equal(t, createdAt, decoded.CreatedAt)

	_, err = decoded.ScanDestinations([]string{"id", "id"})
	require.EqualError(t, err, "duplicate result column \"id\"")
}

type scanSource struct {
	id        int64
	email     string
	createdAt any
}

func (s scanSource) Scan(destinations ...any) error {
	*destinations[0].(*int64) = s.id
	*destinations[1].(**string) = &s.email
	if scanner, ok := destinations[2].(interface{ Scan(any) error }); ok {
		return scanner.Scan(s.createdAt)
	}
	*destinations[2].(*time.Time) = s.createdAt.(time.Time)
	return nil
}

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

// rejectedUsageSource must not compile. It names a column field that does not
// exist and passes a column name where a query.Column is required.
const rejectedUsageSource = `package rejected

import (
	"context"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
)

func Rejected(ctx context.Context, client rasql.Client) {
	users := generated.Users()
	_, _ = rasql.SelectFrom(users).WhereEqual(users.Emial, 42).One(ctx, client)
	_, _ = rasql.SelectFrom(users).WhereEqual("id", 42).One(ctx, client)
}
`

func TestSchemaIsDeterministicAndCompiles(t *testing.T) {
	users := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}, Nullable: true},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
	}
	orders := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	invoices := schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.Schema("generated", users, orders, invoices)
	require.NoError(t, err)
	require.Contains(t, string(source), "type OrdersRow struct")
	require.Contains(t, string(source), "type UsersRow struct")
	require.Contains(t, string(source), "type InvoicesRow struct")
	require.Regexp(t, `(?m)^\s*Amount\s+string$`, string(source))
	require.Regexp(t, `(?m)^\s*TaxRate\s+\*string$`, string(source))
	require.Contains(t, string(source), `{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},`)
	require.Contains(t, string(source), `{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},`)
	require.Contains(t, string(source), "Email")
	require.Contains(t, string(source), "CreatedAt")
	require.NotContains(t, string(source), "rasql:\"", "generated row types state their mapping in methods, not tags")
	require.Contains(t, string(source), "func (r *UsersRow) DecodeRow(src row.Dynamic) error {")
	require.Contains(t, string(source), "if err := row.Assign(src, \"id\", &r.ID); err != nil {")
	require.Contains(t, string(source), "if err := row.Assign(src, \"email\", &r.Email); err != nil {")
	require.Contains(t, string(source), "\treturn row.Assign(src, \"created_at\", &r.CreatedAt)\n")
	require.Contains(t, string(source), "type usersTimeScanner func(any) error")
	require.Contains(t, string(source), "func (r *UsersRow) ScanRow(src row.ScanSource) error {")
	require.Contains(t, string(source), "timeScanner2 := usersTimeScanner(func(value any) error {")
	require.Contains(t, string(source), "return src.Scan(&r.ID, &r.Email, &timeScanner2)")
	require.Contains(t, string(source), "func (r *UsersRow) ScanDestinations(columns []string) ([]any, error) {")
	require.Contains(t, string(source), "\tvar scanned uint64\n")
	require.Contains(t, string(source), "scanned |= uint64(1) << 0")
	require.Contains(t, string(source), "\t\tcase \"created_at\":")
	require.Contains(t, string(source), "\t\t\tdestinations[index] = &timeScanner2")
	require.Contains(t, string(source), "func (r UsersRow) ColumnValue(name string) (any, bool) {")
	require.Contains(t, string(source), "\tcase \"created_at\":\n\t\treturn r.CreatedAt, true\n")
	require.Contains(t, string(source), "\treturn nil, false\n")
	// A nullable column is a pointer field, and DecodeRow must assign through it.
	require.Contains(t, string(source), "\tEmail     *string\n")
	require.Contains(t, string(source), "\"github.com/lestrrat-go/rasql/query\"")
	require.Contains(t, string(source), "\"github.com/lestrrat-go/rasql/row\"")
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

	repeated, err := generate.Schema("generated", invoices, orders, users)
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

	// docs/06-rasqlgen.md promises that a misspelled column field and a column
	// named by string both fail to compile. Build a package that does each and
	// require the compiler to say so, so the documentation cannot drift.
	rejected := filepath.Join(directory, "rejected")
	require.NoError(t, os.MkdirAll(rejected, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rejected, "rejected.go"), []byte(rejectedUsageSource), 0o600))

	command = exec.CommandContext(t.Context(), "go", "build", "./rejected")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.Errorf(t, err, "misspelled column field compiled:\n%s", output)
	require.Contains(t, string(output), "users.Emial undefined")
	require.Contains(t, string(output), "as query.Column value")
}

func TestSchemaUsesMaskWordsForWideRows(t *testing.T) {
	columns := make([]schema.Column, 65)
	for index := range columns {
		columns[index] = schema.Column{Name: "column_" + strconv.Itoa(index), Type: schema.IntegerType{}}
	}

	source, err := generate.Schema("generated", schema.Table{
		Name:       "wide",
		Columns:    columns,
		PrimaryKey: []string{"column_0"},
	})
	require.NoError(t, err)
	require.Contains(t, string(source), "\tvar scanned [2]uint64\n")
	require.Contains(t, string(source), "scanned[1]&(uint64(1)<<0)")
}

// TestSchemaGeneratesDecimalColumns pins the generator's decimal mapping in
// isolation: a DecimalType column becomes a Go string field, and the
// descriptor literal restates Precision and Scale in declaration order.
func TestSchemaGeneratesDecimalColumns(t *testing.T) {
	invoices := schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},
			{Name: "quantity", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(0)}},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.Schema("generated", invoices)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Amount\s+string$`, string(source))
	require.Regexp(t, `(?m)^\s*TaxRate\s+\*string$`, string(source))
	require.Contains(t, string(source), `{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},`)
	require.Contains(t, string(source), `{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},`)
	// A stated scale of zero must survive generation: emitting nothing would
	// leave the regenerated descriptor stating no scale at all.
	require.Contains(t, string(source), `{Name: "quantity", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(0)}},`)
}

// TestSchemaGeneratesUnsignedIntegerColumns pins the generator's signedness
// mapping. An unsigned integer column reaches 18446744073709551615, which
// int64 cannot hold, so its row field is a uint64; a signed one keeps int64.
// The descriptor literal restates Unsigned, so regenerating from the generated
// source produces the same column rather than a signed one.
func TestSchemaGeneratesUnsignedIntegerColumns(t *testing.T) {
	events := schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "sequence", Type: schema.IntegerType{}},
			{Name: "parent_id", Type: schema.IntegerType{Unsigned: true}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.Schema("generated", events)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+uint64$`, string(source))
	require.Regexp(t, `(?m)^\s*Sequence\s+int64$`, string(source))
	require.Regexp(t, `(?m)^\s*ParentID\s+\*uint64$`, string(source))
	require.Contains(t, string(source), `{Name: "id", Type: schema.IntegerType{Unsigned: true}},`)
	require.Contains(t, string(source), `{Name: "sequence", Type: schema.IntegerType{}},`)
	require.Contains(t, string(source), `{Name: "parent_id", Type: schema.IntegerType{Unsigned: true}, Nullable: true},`)
}

func TestSchemaRejectsInvalidPackageName(t *testing.T) {
	_, err := generate.Schema("not-valid")
	require.Error(t, err)
}

func TestSchemaRejectsReservedColumnFieldName(t *testing.T) {
	for _, columnName := range []string{"table", "as", "query_table", "column", "decode_row", "column_value"} {
		t.Run(columnName, func(t *testing.T) {
			_, err := generate.Schema("generated", schema.Table{
				Name: "users",
				Columns: []schema.Column{
					{Name: "id", Type: schema.IntegerType{}},
					{Name: columnName, Type: schema.TextType{}},
				},
				PrimaryKey: []string{"id"},
			})
			require.ErrorContains(t, err, "reserved generated field")
			require.ErrorContains(t, err, columnName)
			require.ErrorContains(t, err, "users")
		})
	}
}

func TestSchemaAllowsScanColumns(t *testing.T) {
	source, err := generate.Schema("generated", schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "scan_columns", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	require.Contains(t, string(source), "ScanColumns string")
}

func TestSchemaRejectsCollidingGeneratedNames(t *testing.T) {
	_, err := generate.Schema("generated",
		schema.Table{
			Name:       "users",
			Columns:    []schema.Column{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
		schema.Table{
			Name:       "users_table",
			Columns:    []schema.Column{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
	)
	require.ErrorContains(t, err, "duplicates generated name")
	require.ErrorContains(t, err, "UsersTable")
}

func TestSchemaAppliesInitialismsToTableNames(t *testing.T) {
	apiKeys := schema.Table{
		Name: "api_keys",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "api_key", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.Schema("generated", apiKeys)
	require.NoError(t, err)
	require.Contains(t, string(source), "func APIKeys() APIKeysTable {")
	require.Contains(t, string(source), "type APIKeysRow struct {")
	require.Contains(t, string(source), "var apiKeysTable = newAPIKeysTable(rasql.MustTable[APIKeysRow](")

	// The api_key column must spell "API" the same way the api_keys table
	// does, so the package does not expose the same word two ways.
	require.Contains(t, string(source), "\tAPIKey string\n")
}

func TestSchemaRejectsCollidingInitialismNames(t *testing.T) {
	_, err := generate.Schema("generated",
		schema.Table{
			Name:       "api_keys",
			Columns:    []schema.Column{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
		schema.Table{
			Name:       "APIKeys",
			Columns:    []schema.Column{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
	)
	require.ErrorContains(t, err, "duplicates generated name")
	require.ErrorContains(t, err, "APIKeys")
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
