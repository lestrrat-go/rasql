package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

var _ row.GeneratedRow = (*generated.UsersRow)(nil)

func TestGeneratedRowMapsItsOwnColumns(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := row.NewDynamic(
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
	nullEmail, err := row.NewDynamic(
		[]string{"id", "email", "created_at"},
		[]any{int64(7), nil, createdAt},
	)
	require.NoError(t, err)
	decoded, err = row.Decode[generated.UsersRow](nullEmail)
	require.NoError(t, err)
	require.Nil(t, decoded.Email)

	// A missing column is reported by the generated DecodeRow.
	partial, err := row.NewDynamic([]string{"id"}, []any{int64(7)})
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
	require.Equal(t, "users", users.Ref().Name())
}

func TestGeneratedAsRebindsColumns(t *testing.T) {
	users := generated.Users()
	manager, err := users.As("manager")
	require.NoError(t, err)
	require.Equal(t, "manager", manager.Ref().Qualifier())
	require.Equal(t, "manager", manager.ID.Source().Qualifier())
	require.Equal(t, "manager", manager.Email.Source().Qualifier())

	_, err = users.As("not an identifier")
	require.Error(t, err)
}

func TestGeneratedSelfJoinRendersAlias(t *testing.T) {
	users := generated.Users()
	manager, err := users.As("manager")
	require.NoError(t, err)

	statement, err := query.NewSelect(users.Ref(), query.Project(users.ID))
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

func TestGeneratedRelationships(t *testing.T) {
	orders := generated.Orders()
	belongsTo := orders.User()
	require.Equal(t, "users", belongsTo.Parent.Ref().Name())
	require.Equal(t, "orders", belongsTo.Child.Ref().Name())
	require.Equal(t, "id", belongsTo.ParentKey.Name())
	require.Equal(t, "user_id", belongsTo.ChildKey.Name())

	hasMany := generated.Users().Orders()
	require.Equal(t, "users", hasMany.Parent.Ref().Name())
	require.Equal(t, "orders", hasMany.Child.Ref().Name())
}
`

// rejectedUsageSource must not compile. It names a column field that does not
// exist and passes a column name where a query.ColumnRef is required.
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
	users := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}, Nullable: true},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}
	invoices := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.PackageSource("generated", users, orders, invoices)
	require.NoError(t, err)
	require.Contains(t, string(source), "type OrdersRow struct")
	require.Contains(t, string(source), "type UsersRow struct")
	require.Contains(t, string(source), "type InvoicesRow struct")
	require.Regexp(t, `(?m)^\s*Amount\s+string$`, string(source))
	require.Regexp(t, `(?m)^\s*TaxRate\s+\*string$`, string(source))
	require.Contains(t, string(source), `schema.Decimal("amount", 19, 4),`)
	require.Contains(t, string(source), `schema.Decimal("tax_rate", 5, 4, schema.Nullable()),`)
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
	require.Contains(t, string(source), "\tCreatedAt query.ColumnRef\n")
	require.Contains(t, string(source), "func newUsersTable(table rasql.Table[UsersRow]) UsersTable {")
	require.Contains(t, string(source), "CreatedAt: rasql.MustColumn(table, \"created_at\"),")
	require.Contains(t, string(source), "var ordersTable = newOrdersTable(rasql.MustTableOf[OrdersRow](schema.MustTableDef(\"orders\",")
	require.Contains(t, string(source), "var usersTable = newUsersTable(rasql.MustTableOf[UsersRow](schema.MustTableDef(\"users\",")
	require.Contains(t, string(source), "func Orders() OrdersTable {")
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.Contains(t, string(source), "type OrdersTableUserRelation struct {")
	require.Contains(t, string(source), "func (t OrdersTable) User() OrdersTableUserRelation {")
	require.Contains(t, string(source), "func (t UsersTable) Orders() UsersTableOrdersRelation {")
	require.Contains(t, string(source), "func (t UsersTable) As(alias string) (UsersTable, error) {")
	require.NotContains(t, string(source), "var Users =")
	require.Less(t, stringIndex(t, source, "var ordersTable"), stringIndex(t, source, "var usersTable"))

	repeated, err := generate.PackageSource("generated", invoices, orders, users)
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
	require.Contains(t, string(output), "as query.ColumnRef value")
}

func TestSchemaUsesMaskWordsForWideRows(t *testing.T) {
	columns := make([]schema.ColumnDef, 65)
	for index := range columns {
		columns[index] = schema.ColumnDef{Name: "column_" + strconv.Itoa(index), Type: schema.IntegerType{}}
	}

	source, err := generate.PackageSource("generated", schema.TableDef{
		Name:       "wide",
		Columns:    columns,
		PrimaryKey: []string{"column_0"},
	})
	require.NoError(t, err)
	require.Contains(t, string(source), "\tvar scanned [2]uint64\n")
	require.Contains(t, string(source), "scanned[1]&(uint64(1)<<0)")
}

const generatedRelationshipUsageTest = `package generated_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"example.com/generated"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

type recordingExecutor struct {
	statement render.Statement
}

func (e *recordingExecutor) Dialect() dialect.Dialect {
	return dialect.PostgreSQL()
}

func (e *recordingExecutor) QueryRendered(_ context.Context, statement render.Statement) (*sql.Rows, error) {
	e.statement = statement
	return nil, errors.New("query recorded")
}

func (e *recordingExecutor) ExecRendered(_ context.Context, _ render.Statement) (sql.Result, error) {
	return nil, errors.New("exec not supported")
}

func TestGeneratedRelationships(t *testing.T) {
	users := generated.Users()
	orders := generated.Orders()
	require.Equal(t, "tenant", users.Ref().Schema())
	require.Equal(t, "tenant", orders.Ref().Schema())

	belongsTo := orders.User()
	require.Equal(t, "tenant", belongsTo.Parent.Ref().Schema())
	require.Equal(t, "tenant", belongsTo.Child.Ref().Schema())
	require.Equal(t, "id", belongsTo.ParentKey.Name())
	require.Equal(t, "user_id", belongsTo.ChildKey.Name())
	require.Equal(t, query.JoinInner, belongsTo.Join().Type())
	require.Equal(t, "tenant", belongsTo.Join().Source().Schema())

	belongsToExecutor := &recordingExecutor{}
	_, err := belongsTo.Load(t.Context(), belongsToExecutor, []generated.OrdersRow{{UserID: 7}})
	require.EqualError(t, err, "query recorded")
	require.Equal(t, "SELECT \"tenant\".\"users\".\"id\" FROM \"tenant\".\"users\" WHERE (\"tenant\".\"users\".\"id\" IN ($1))", belongsToExecutor.statement.SQL())

	hasMany := users.Orders()
	require.Equal(t, "id", hasMany.ParentKey.Name())
	require.Equal(t, "user_id", hasMany.ChildKey.Name())
	require.Equal(t, query.JoinInner, hasMany.Join().Type())
	require.Equal(t, "tenant", hasMany.Join().Source().Schema())

	hasManyExecutor := &recordingExecutor{}
	_, err = hasMany.Load(t.Context(), hasManyExecutor, []generated.UsersRow{{ID: 7}})
	require.EqualError(t, err, "query recorded")
	require.Equal(t, "SELECT \"tenant\".\"orders\".\"id\", \"tenant\".\"orders\".\"user_id\" FROM \"tenant\".\"orders\" WHERE (\"tenant\".\"orders\".\"user_id\" IN ($1))", hasManyExecutor.statement.SQL())

	aliasedUsers, err := users.As("u")
	require.NoError(t, err)
	require.Equal(t, "u", aliasedUsers.Orders().ParentKey.Source().Qualifier())
	aliasedOrders, err := orders.As("o")
	require.NoError(t, err)
	require.Equal(t, "o", aliasedOrders.User().ChildKey.Source().Qualifier())
}
`

const generatedSelfReferentialRelationshipUsageTest = `package generated_test

import (
	"testing"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

func TestGeneratedSelfReferentialRelationshipsRender(t *testing.T) {
	employees := generated.Employees()
	manager := employees.Manager()
	statement, err := rasql.SelectFrom(employees).Join(manager.Join()).Build(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, ` + "`" + `SELECT "employees"."id", "employees"."manager_id" FROM "employees" INNER JOIN "employees" AS "employees_manager_parent" ON ("employees_manager_parent"."id" = "employees"."manager_id")` + "`" + `, statement.SQL())

	children := employees.Employees()
	statement, err = rasql.SelectFrom(employees).Join(children.Join()).Build(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, ` + "`" + `SELECT "employees"."id", "employees"."manager_id" FROM "employees" INNER JOIN "employees" AS "employees_employees_child" ON ("employees"."id" = "employees_employees_child"."manager_id")` + "`" + `, statement.SQL())
}
`

func TestSchemaGeneratesTypedRelationships(t *testing.T) {
	users := schema.TableDef{
		Schema:     "tenant",
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Schema: "tenant",
		Name:   "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_user_id_fkey",
			Columns:           []string{"user_id"},
			ReferencedSchema:  "tenant",
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	source, err := generate.PackageSource("generated", users, orders)
	require.NoError(t, err)
	usersSource, err := generate.TableSource("generated", users, users, orders)
	require.NoError(t, err)
	ordersSource, err := generate.TableSource("generated", orders, users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "func (t OrdersTable) User() OrdersTableUserRelation")
	require.Contains(t, string(source), "func (t UsersTable) Orders() UsersTableOrdersRelation")
	require.Contains(t, string(source), "func (r UsersTableOrdersRelation) Load")
	require.Contains(t, string(source), `schema.RelationshipNamed("User")`)
	require.Contains(t, string(usersSource), "func (t UsersTable) Orders() UsersTableOrdersRelation")
	require.Contains(t, string(ordersSource), "func (t OrdersTable) User() OrdersTableUserRelation")

	directory, err := os.MkdirTemp(".", ".tmp-relationship-schema-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "users_gen.go"), usersSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "orders_gen.go"), ordersSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(generatedRelationshipUsageTest), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => ../..\n"), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}

func TestSchemaGeneratesDistinctInverseRelationships(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	memberships := schema.TableDef{
		Name: "memberships",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "billing_user_id", Type: schema.IntegerType{}},
			{Name: "shipping_user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{
			{Columns: []string{"billing_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
			{Columns: []string{"shipping_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
		},
	}

	source, err := generate.PackageSource("generated", users, memberships)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "func (t UsersTable) Memberships() UsersTableMembershipsRelation")
	require.Contains(t, text, "func (t UsersTable) ShippingUserMemberships() UsersTableShippingUserMembershipsRelation")
	require.Contains(t, text, "func (r UsersTableMembershipsRelation) Join() query.Join")
	require.Contains(t, text, "func (r UsersTableMembershipsRelation) Load(ctx context.Context, x rasql.Executor, parents []UsersRow)")
	require.Contains(t, text, "func (r UsersTableShippingUserMembershipsRelation) Join() query.Join")
	require.Contains(t, text, "func (r UsersTableShippingUserMembershipsRelation) Load(ctx context.Context, x rasql.Executor, parents []UsersRow)")
}

func TestSchemaKeepsInverseMethodsStableWhenForeignKeysReorder(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	generateSource := func(foreignKeys []schema.ForeignKeyDef) string {
		memberships := schema.TableDef{
			Name: "memberships",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "billing_user_id", Type: schema.IntegerType{}},
				{Name: "shipping_user_id", Type: schema.IntegerType{}},
			},
			PrimaryKey:  []string{"id"},
			ForeignKeys: foreignKeys,
		}
		source, err := generate.PackageSource("generated", users, memberships)
		require.NoError(t, err)
		return string(source)
	}
	foreignKeys := []schema.ForeignKeyDef{
		{Columns: []string{"billing_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
		{Columns: []string{"shipping_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
	}

	for _, source := range []string{
		generateSource(foreignKeys),
		generateSource([]schema.ForeignKeyDef{foreignKeys[1], foreignKeys[0]}),
	} {
		memberships := generatedMethodBlock(t, source, "func (t UsersTable) Memberships() UsersTableMembershipsRelation")
		require.Contains(t, memberships, "ChildKey: child.BillingUserID")
		shipping := generatedMethodBlock(t, source, "func (t UsersTable) ShippingUserMemberships() UsersTableShippingUserMembershipsRelation")
		require.Contains(t, shipping, "ChildKey: child.ShippingUserID")
	}
}

func TestSchemaGeneratesSelfReferentialInverseRelationship(t *testing.T) {
	employees := schema.TableDef{
		Name: "employees",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "manager_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"manager_id"},
			ReferencedTable:   "employees",
			ReferencedColumns: []string{"id"},
		}},
	}

	source, err := generate.PackageSource("generated", employees)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "func (t EmployeesTable) Manager() EmployeesTableManagerRelation")
	require.Contains(t, text, "func (t EmployeesTable) Employees() EmployeesTableEmployeesRelation")
	require.Contains(t, text, "func (r EmployeesTableEmployeesRelation) Load(ctx context.Context, x rasql.Executor, parents []EmployeesRow)")
}

func TestSchemaGeneratesSelfReferentialRenderedJoins(t *testing.T) {
	employees := schema.TableDef{
		Name: "employees",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "manager_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"manager_id"},
			ReferencedTable:   "employees",
			ReferencedColumns: []string{"id"},
		}},
	}

	source, err := generate.PackageSource("generated", employees)
	require.NoError(t, err)
	directory, err := os.MkdirTemp(".", ".tmp-self-relationship-schema-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(generatedSelfReferentialRelationshipUsageTest), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => ../..\n"), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
}

func TestSchemaRenamesReservedInverseRelationship(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	aliases := schema.TableDef{
		Name: "as",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	source, err := generate.PackageSource("generated", users, aliases)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "func (t UsersTable) UserAs() UsersTableUserAsRelation")
	require.Contains(t, text, "func (r UsersTableUserAsRelation) Load(ctx context.Context, x rasql.Executor, parents []UsersRow)")
}

func generatedMethodBlock(t *testing.T, source, signature string) string {
	t.Helper()
	start := strings.Index(source, signature)
	require.GreaterOrEqual(t, start, 0)
	rest := source[start:]
	next := strings.Index(rest[len(signature):], "\nfunc ")
	if next < 0 {
		return rest
	}
	return rest[:len(signature)+next]
}

func TestSchemaMergesExplicitAndDerivedRelationships(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	memberships := schema.TableDef{
		Name: "memberships",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "billing_user_id", Type: schema.IntegerType{}},
			{Name: "shipping_user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{
			{Columns: []string{"billing_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
			{Columns: []string{"shipping_user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
		},
		Relationships: []schema.RelationshipDef{{
			Name:              "BillingUser",
			Kind:              schema.RelationshipBelongsTo,
			Columns:           []string{"billing_user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	source, err := generate.PackageSource("generated", users, memberships)
	require.NoError(t, err)
	text := string(source)
	for _, expected := range []string{
		"func (t MembershipsTable) BillingUser() MembershipsTableBillingUserRelation",
		"func (t MembershipsTable) ShippingUser() MembershipsTableShippingUserRelation",
		"func (t UsersTable) Memberships() UsersTableMembershipsRelation",
		"func (t UsersTable) ShippingUserMemberships() UsersTableShippingUserMembershipsRelation",
		"func (r MembershipsTableBillingUserRelation) Load(ctx context.Context, x rasql.Executor, children []MembershipsRow)",
		"func (r MembershipsTableShippingUserRelation) Load(ctx context.Context, x rasql.Executor, children []MembershipsRow)",
	} {
		require.Contains(t, text, expected)
	}
}

// TestSchemaGeneratesDecimalColumns pins the generator's decimal mapping in
// isolation: a DecimalType column becomes a Go string field, and the
// generated schema.Decimal call restates precision and scale positionally.
func TestSchemaGeneratesDecimalColumns(t *testing.T) {
	invoices := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true},
			{Name: "quantity", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(0)}},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.PackageSource("generated", invoices)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Amount\s+string$`, string(source))
	require.Regexp(t, `(?m)^\s*TaxRate\s+\*string$`, string(source))
	require.Contains(t, string(source), `schema.Decimal("amount", 19, 4),`)
	require.Contains(t, string(source), `schema.Decimal("tax_rate", 5, 4, schema.Nullable()),`)
	// A stated scale of zero must survive generation: emitting nothing would
	// leave the regenerated descriptor stating no scale at all.
	require.Contains(t, string(source), `schema.Decimal("quantity", 10, 0),`)
}

// TestSchemaGeneratesUnsignedIntegerColumns pins the generator's signedness
// mapping. An unsigned integer column reaches 18446744073709551615, which
// int64 cannot hold, so its row field is a uint64; a signed one keeps int64.
// The generated schema.Integer call restates schema.Unsigned(), so
// regenerating from the generated source produces the same column rather
// than a signed one.
func TestSchemaGeneratesUnsignedIntegerColumns(t *testing.T) {
	events := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "sequence", Type: schema.IntegerType{}},
			{Name: "parent_id", Type: schema.IntegerType{Unsigned: true}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.PackageSource("generated", events)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+uint64$`, string(source))
	require.Regexp(t, `(?m)^\s*Sequence\s+int64$`, string(source))
	require.Regexp(t, `(?m)^\s*ParentID\s+\*uint64$`, string(source))
	require.Contains(t, string(source), `schema.Integer("id", schema.Unsigned()),`)
	require.Contains(t, string(source), `schema.Integer("sequence"),`)
	require.Contains(t, string(source), `schema.Integer("parent_id", schema.Unsigned(), schema.Nullable()),`)
}

// TestSchemaGeneratesTextWidthColumns pins the generator's text-width
// mapping. A stated width restates schema.Width(n) in the generated
// schema.Text call, so regenerating from the generated source produces the
// same column rather than an unbounded one; an unstated width emits no
// option at all, exactly as it did before TextType had one.
func TestSchemaGeneratesTextWidthColumns(t *testing.T) {
	users := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}},
			{Name: "bio", Type: schema.TextType{}, Nullable: true},
			{Name: "flag", Type: schema.TextType{Width: schema.NewTextWidth(0)}},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.PackageSource("generated", users)
	require.NoError(t, err)
	require.Contains(t, string(source), `schema.Text("email", schema.Width(255)),`)
	require.Contains(t, string(source), `schema.Text("bio", schema.Nullable()),`)
	// A stated width of zero must survive generation the same way a stated
	// decimal scale of zero does: emitting nothing would leave the
	// regenerated descriptor stating no width at all.
	require.Contains(t, string(source), `schema.Text("flag", schema.Width(0)),`)
}

func TestSchemaRejectsInvalidPackageName(t *testing.T) {
	_, err := generate.PackageSource("not-valid")
	require.Error(t, err)
}

func TestSchemaRejectsReservedColumnFieldName(t *testing.T) {
	for _, columnName := range []string{"table", "as", "ref", "column", "decode_row", "column_value"} {
		t.Run(columnName, func(t *testing.T) {
			_, err := generate.PackageSource("generated", schema.TableDef{
				Name: "users",
				Columns: []schema.ColumnDef{
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

func TestSchemaRejectsCollidingRelationshipMethodNames(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
		Relationships: []schema.RelationshipDef{
			{
				Name:              "BillingUser",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"user_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
			},
			{
				Name:              "billing_user",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"user_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
			},
		},
	}

	_, err := generate.PackageSource("generated", users, orders)
	require.ErrorContains(t, err, `relationships[0] "BillingUser"`)
	require.ErrorContains(t, err, `relationships[1] "billing_user"`)
	require.ErrorContains(t, err, `duplicate generated method "BillingUser"`)
}

func TestSchemaAllowsScanColumns(t *testing.T) {
	source, err := generate.PackageSource("generated", schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "scan_columns", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	require.Contains(t, string(source), "ScanColumns string")
}

func TestSchemaRejectsCollidingGeneratedNames(t *testing.T) {
	_, err := generate.PackageSource("generated",
		schema.TableDef{
			Name:       "users",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
		schema.TableDef{
			Name:       "users_table",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
	)
	require.ErrorContains(t, err, "duplicates generated name")
	require.ErrorContains(t, err, "UsersTable")
}

func TestSchemaRejectsRelationshipTypeCollisions(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}
	collision := schema.TableDef{
		Name:       "users_table_orders_relation",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	_, err := generate.PackageSource("generated", users, orders, collision)
	require.ErrorContains(t, err, `relationship "Orders" on table "users"`)
	require.ErrorContains(t, err, `UsersTableOrdersRelation`)
}

func TestSchemaRejectsReservedRelationshipMethod(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
		Relationships: []schema.RelationshipDef{{
			Name:              "as",
			Kind:              schema.RelationshipBelongsTo,
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	err := generate.Validate("generated", users, orders)
	require.ErrorContains(t, err, `relationship "as" on table "orders" uses reserved generated method "As"`)
}

func TestSchemaAllowsReservedMethodNameForNullableRelationship(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	orders := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "as_id", Type: schema.IntegerType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"as_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	source, err := generate.PackageSource("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "AsID *int64")
	require.NotContains(t, string(source), "func (t OrdersTable) As() OrdersTableAsRelation")
}

func TestSchemaAppliesInitialismsToTableNames(t *testing.T) {
	apiKeys := schema.TableDef{
		Name: "api_keys",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "api_key", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := generate.PackageSource("generated", apiKeys)
	require.NoError(t, err)
	require.Contains(t, string(source), "func APIKeys() APIKeysTable {")
	require.Contains(t, string(source), "type APIKeysRow struct {")
	require.Contains(t, string(source), "var apiKeysTable = newAPIKeysTable(rasql.MustTableOf[APIKeysRow](")

	// The api_key column must spell "API" the same way the api_keys table
	// does, so the package does not expose the same word two ways.
	require.Contains(t, string(source), "\tAPIKey string\n")
}

func TestSchemaRejectsCollidingInitialismNames(t *testing.T) {
	_, err := generate.PackageSource("generated",
		schema.TableDef{
			Name:       "api_keys",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
		schema.TableDef{
			Name:       "APIKeys",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
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
