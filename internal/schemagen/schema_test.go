package schemagen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// generatedUsageTest exercises the emitted column accessors and As method
// from inside the temporary module, so a defect in the generated source
// fails here rather than in a consumer.
const generatedUsageTest = `package generated_test

import (
	"testing"
	"time"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

var _ rasql.Scanner = (*generated.UsersRow)(nil)
var _ rasql.DestinationScanner = (*generated.UsersRow)(nil)

func TestGeneratedRowDecodesByFieldName(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := dynamic.NewRow(
		[]string{"id", "email", "created_at"},
		[]any{int64(7), "ada@example.com", createdAt},
	)
	require.NoError(t, err)

	// UsersRow carries no rasql tags, so dynamic.Decode's field-mapping
	// fallback snake-cases ID, Email and CreatedAt onto id, email and created_at.
	decoded, err := dynamic.Decode[generated.UsersRow](result)
	require.NoError(t, err)
	require.Equal(t, int64(7), decoded.ID)
	require.NotNil(t, decoded.Email)
	require.Equal(t, "ada@example.com", *decoded.Email)
	require.Equal(t, createdAt, decoded.CreatedAt)

	// A nullable column decodes into a nil pointer rather than failing.
	nullEmail, err := dynamic.NewRow(
		[]string{"id", "email", "created_at"},
		[]any{int64(7), nil, createdAt},
	)
	require.NoError(t, err)
	decoded, err = dynamic.Decode[generated.UsersRow](nullEmail)
	require.NoError(t, err)
	require.Nil(t, decoded.Email)

	// A missing column is reported by the field-mapping fallback.
	partial, err := dynamic.NewRow([]string{"id"}, []any{int64(7)})
	require.NoError(t, err)
	_, err = dynamic.Decode[generated.UsersRow](partial)
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
	var scanner rasql.Scanner = &generated.UsersRow{}

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
	require.Equal(t, "id", users.ID().Name())
	require.Equal(t, "email", users.Email().Name())
	require.Equal(t, "created_at", users.CreatedAt().Name())
	require.Equal(t, "users", users.ID().Source().Qualifier())
	require.Equal(t, "users", users.Ref().Name())
}

func TestGeneratedAsQualifiesColumns(t *testing.T) {
	users := generated.Users()
	manager, err := users.As("manager")
	require.NoError(t, err)
	require.Equal(t, "manager", manager.Ref().Qualifier())
	require.Equal(t, "manager", manager.ID().Source().Qualifier())
	require.Equal(t, "manager", manager.Email().Source().Qualifier())

	_, err = users.As("not an identifier")
	require.Error(t, err)
}

func TestGeneratedSelfJoinRendersAlias(t *testing.T) {
	users := generated.Users()
	manager, err := users.As("manager")
	require.NoError(t, err)

	statement, err := query.NewSelect(users.Ref(), query.Project(users.ID()))
	require.NoError(t, err)
	statement, err = statement.WithJoin(rasql.InnerJoin(manager, query.Equal(users.ID(), manager.ID())))
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

func Rejected(ctx context.Context, db rasql.DB) {
	users := generated.Users()
	_, _ = rasql.SelectFrom(users).WhereEqual(users.Emial, 42).One(ctx, db)
	_, _ = rasql.SelectFrom(users).WhereEqual("id", 42).One(ctx, db)
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

	source, err := schemagen.PackageSource("generated", users, orders, invoices)
	require.NoError(t, err)
	require.Contains(t, string(source), "type OrdersRow struct")
	require.Contains(t, string(source), "type UsersRow struct")
	require.Contains(t, string(source), "type InvoicesRow struct")
	require.Regexp(t, `(?m)^\s*Amount\s+string$`, string(source))
	require.Regexp(t, `(?m)^\s*TaxRate\s+\*string$`, string(source))
	require.Contains(t, string(source), "Email")
	require.Contains(t, string(source), "CreatedAt")
	require.NotContains(t, string(source), "rasql:\"", "generated row types state their mapping in methods, not tags")
	require.Contains(t, string(source), "type usersTimeScanner func(any) error")
	require.Contains(t, string(source), "func (r *UsersRow) ScanRow(src rasql.ScanSource) error {")
	require.Contains(t, string(source), "timeScanner2 := usersTimeScanner(func(value any) error {")
	require.Contains(t, string(source), "\t\treturn rasql.ScanValue(&r.CreatedAt, value)\n")
	require.NotContains(t, string(source), "row.NewDynamic")
	require.Contains(t, string(source), "return src.Scan(&r.ID, &r.Email, &timeScanner2)")
	require.Contains(t, string(source), "func (r *UsersRow) ScanDestinations(columns []string) ([]any, error) {")
	require.Contains(t, string(source), "\tscanned := rasql.NewScanMask(3)\n")
	require.Contains(t, string(source), "\t\tscanIndexID = iota\n\t\tscanIndexEmail\n\t\tscanIndexCreatedAt\n")
	require.Contains(t, string(source), "if !scanned.Mark(scanIndexID) {")
	require.Contains(t, string(source), "\t\tcase \"created_at\":")
	require.Contains(t, string(source), "\t\t\tdestinations[index] = &timeScanner2")
	require.Contains(t, string(source), "func (r UsersRow) ColumnValue(name string) (any, bool) {")
	require.Contains(t, string(source), "\tcase \"created_at\":\n\t\treturn r.CreatedAt, true\n")
	require.Contains(t, string(source), "\treturn nil, false\n")
	// A nullable column is a pointer field, and the generated scan methods
	// assign through it.
	require.Contains(t, string(source), "\tEmail     *string\n")
	require.Contains(t, string(source), "\"github.com/lestrrat-go/rasql/query\"")
	require.NotContains(t, string(source), "github.com/lestrrat-go/rasql/row")
	// PackageSource returns a whole package, descriptors included, so it
	// names the schema package its descriptor literals are written in.
	require.Contains(t, string(source), "\"github.com/lestrrat-go/rasql/schema\"")
	require.Contains(t, string(source), "var usersDef = schema.TableDef{")
	require.Contains(t, string(source), "var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}")
	require.Contains(t, string(source), "\"github.com/lestrrat-go/rasql\"")
	require.Contains(t, string(source), "type UsersTable struct {\n\trasql.Table[UsersRow]\n}\n")
	require.Contains(t, string(source), `func (t UsersTable) CreatedAt() query.ColumnRef { return rasql.ColumnOf(t.Table, "created_at") }`)
	require.NotContains(t, string(source), "newUsersTable")
	require.Contains(t, string(source), "func Orders() OrdersTable {")
	require.Contains(t, string(source), "func Users() UsersTable {")
	require.Contains(t, string(source), "type OrdersTableUserRelation struct {")
	require.Contains(t, string(source), "func (t OrdersTable) User() OrdersTableUserRelation {")
	require.Contains(t, string(source), "func (t UsersTable) Orders() UsersTableOrdersRelation {")
	require.Contains(t, string(source), "func (t UsersTable) As(alias string) (UsersTable, error) {")
	require.NotContains(t, string(source), "var Users =")

	repeated, err := schemagen.PackageSource("generated", invoices, orders, users)
	require.NoError(t, err)
	require.Equal(t, string(source), string(repeated), "generated source must not depend on input order")

	descriptorSource, err := schemagen.DescriptorSource("generated", users, orders, invoices)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), "var usersDef = schema.TableDef{")
	require.Contains(t, string(descriptorSource), "var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}")
	require.Contains(t, string(descriptorSource), "var ordersDef = schema.TableDef{")
	require.Contains(t, string(descriptorSource), "var ordersTable = OrdersTable{rasql.TableFrom[OrdersRow](ordersDef)}")
	require.Contains(t, string(descriptorSource), `{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}}`)
	require.Contains(t, string(descriptorSource), `{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true}`)
	require.NotContains(t, string(descriptorSource), "\"fmt\"")
	require.NotContains(t, string(descriptorSource), "\"github.com/lestrrat-go/rasql/query\"")
	require.Less(t, stringIndex(t, descriptorSource, "var ordersDef"), stringIndex(t, descriptorSource, "var usersDef"))
	// Tables clones every table's descriptor after every table's own
	// declaration, in the same alphabetical order those declarations
	// already appear in.
	require.Contains(t, string(descriptorSource), "func Tables() []schema.TableDef {")
	require.Less(t, stringIndex(t, descriptorSource, "var usersDef"), stringIndex(t, descriptorSource, "func Tables()"))
	require.Less(t, stringIndex(t, descriptorSource, "invoicesDef.Clone()"), stringIndex(t, descriptorSource, "ordersDef.Clone()"))
	require.Less(t, stringIndex(t, descriptorSource, "ordersDef.Clone()"), stringIndex(t, descriptorSource, "usersDef.Clone()"))

	descriptorTestSource, err := schemagen.DescriptorTestSource("generated", users, orders, invoices)
	require.NoError(t, err)
	require.Contains(t, string(descriptorTestSource), "func TestRasqlgenGeneratedDefinitionsAreValid(t *testing.T) {")

	directory := t.TempDir()
	// PackageSource's output is the whole package here. Adding
	// DescriptorSource's output beside it would declare every descriptor a
	// second time, which is what makes the two alternatives rather than a
	// pair; the generated descriptor test still compiles against the
	// definitions PackageSource declares.
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen_test.go"), descriptorTestSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(generatedUsageTest), 0o600))
	// The replace directive must be absolute: t.TempDir() no longer nests the
	// scratch module under this package directory, so a relative path would
	// resolve against the temp directory's own location instead of the repo.
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)

	// docs/06-rasqlgen.md promises that a misspelled column accessor and a
	// column named by string both fail to compile. Build a package that does
	// each and require the compiler to say so, so the documentation cannot
	// drift.
	rejected := filepath.Join(directory, "rejected")
	require.NoError(t, os.MkdirAll(rejected, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rejected, "rejected.go"), []byte(rejectedUsageSource), 0o600))

	command = exec.CommandContext(t.Context(), "go", "build", "./rejected")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.Errorf(t, err, "misspelled column accessor compiled:\n%s", output)
	require.Contains(t, string(output), "users.Emial undefined")
	require.Contains(t, string(output), "as query.ColumnRef value")
}

func TestSchemaSizesScanMaskForWideRows(t *testing.T) {
	columns := make([]schema.ColumnDef, 65)
	for index := range columns {
		columns[index] = schema.ColumnDef{Name: "column_" + strconv.Itoa(index), Type: schema.IntegerType{}}
	}

	source, err := schemagen.PackageSource("generated", schema.TableDef{
		Name:       "wide",
		Columns:    columns,
		PrimaryKey: []string{"column_0"},
	})
	require.NoError(t, err)
	// A table wider than one mask word states its column count and nothing
	// else. Splitting that count across words is rasql.ScanMask's job, so no
	// bit arithmetic reaches the generated file.
	require.Contains(t, string(source), "\tscanned := rasql.NewScanMask(65)\n")
	require.Contains(t, string(source), "if !scanned.Mark(scanIndexColumn64) {")
	require.NotContains(t, string(source), "uint64(1)<<")
}

const generatedRelationshipUsageTest = `package generated_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

// recordingHandle is a rasql.Handle that keeps the SQL it was asked to run and
// refuses to run it, so a generated Load can be checked against the statement
// it renders without a database behind it.
type recordingHandle struct {
	query string
}

func (h *recordingHandle) QueryContext(_ context.Context, statement string, _ ...any) (*sql.Rows, error) {
	h.query = statement
	return nil, errors.New("query recorded")
}

func (h *recordingHandle) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, errors.New("exec not supported")
}

// recordingDB pairs a recordingHandle with the dialect the generated fixtures
// are rendered for.
func recordingDB(t *testing.T) (rasql.DB, *recordingHandle) {
	t.Helper()

	handle := &recordingHandle{}
	db, err := rasql.New(handle, dialect.PostgreSQL())
	require.NoError(t, err)
	return db, handle
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

	belongsToDB, belongsToHandle := recordingDB(t)
	_, err := belongsTo.Load(t.Context(), belongsToDB, []generated.OrdersRow{{UserID: 7}})
	require.ErrorContains(t, err, "query recorded")
	require.Equal(t, "SELECT \"tenant\".\"users\".\"id\" FROM \"tenant\".\"users\" WHERE (\"tenant\".\"users\".\"id\" IN ($1))", belongsToHandle.query)

	hasMany := users.Orders()
	require.Equal(t, "id", hasMany.ParentKey.Name())
	require.Equal(t, "user_id", hasMany.ChildKey.Name())
	require.Equal(t, query.JoinInner, hasMany.Join().Type())
	require.Equal(t, "tenant", hasMany.Join().Source().Schema())

	hasManyDB, hasManyHandle := recordingDB(t)
	_, err = hasMany.Load(t.Context(), hasManyDB, []generated.UsersRow{{ID: 7}})
	require.ErrorContains(t, err, "query recorded")
	require.Equal(t, "SELECT \"tenant\".\"orders\".\"id\", \"tenant\".\"orders\".\"user_id\" FROM \"tenant\".\"orders\" WHERE (\"tenant\".\"orders\".\"user_id\" IN ($1))", hasManyHandle.query)

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

	source, err := schemagen.PackageSource("generated", users, orders)
	require.NoError(t, err)
	usersSource, err := schemagen.TableSurfaceSource("generated", users, users, orders)
	require.NoError(t, err)
	ordersSource, err := schemagen.TableSurfaceSource("generated", orders, users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "func (t OrdersTable) User() OrdersTableUserRelation")
	require.Contains(t, string(source), "func (t UsersTable) Orders() UsersTableOrdersRelation")
	require.Contains(t, string(source), "func (r UsersTableOrdersRelation) Load")
	require.Contains(t, string(usersSource), "func (t UsersTable) Orders() UsersTableOrdersRelation")
	// The split-file surface leaves every descriptor to DescriptorSource,
	// which is the whole difference between it and TableSource.
	require.NotContains(t, string(usersSource), "var usersDef = schema.TableDef{")
	require.NotContains(t, string(ordersSource), "var ordersDef = schema.TableDef{")
	require.Contains(t, string(ordersSource), "func (t OrdersTable) User() OrdersTableUserRelation")

	descriptorSource, err := schemagen.DescriptorSource("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `Relationships: []schema.RelationshipDef{`)
	require.Contains(t, string(descriptorSource), `{Name: "User", Kind: schema.RelationshipBelongsTo, Columns: []string{"user_id"}, ReferencedSchema: "tenant", ReferencedTable: "users", ReferencedColumns: []string{"id"}}`)

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "users_gen.go"), usersSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "orders_gen.go"), ordersSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen.go"), descriptorSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(generatedRelationshipUsageTest), 0o600))
	// The replace directive must be absolute: t.TempDir() no longer nests the
	// scratch module under this package directory, so a relative path would
	// resolve against the temp directory's own location instead of the repo.
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))

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

	source, err := schemagen.PackageSource("generated", users, memberships)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "func (t UsersTable) Memberships() UsersTableMembershipsRelation")
	require.Contains(t, text, "func (t UsersTable) ShippingUserMemberships() UsersTableShippingUserMembershipsRelation")
	require.Contains(t, text, "func (r UsersTableMembershipsRelation) Join() query.Join")
	require.Contains(t, text, "func (r UsersTableMembershipsRelation) Load(ctx context.Context, db rasql.DB, parents []UsersRow)")
	require.Contains(t, text, "func (r UsersTableShippingUserMembershipsRelation) Join() query.Join")
	require.Contains(t, text, "func (r UsersTableShippingUserMembershipsRelation) Load(ctx context.Context, db rasql.DB, parents []UsersRow)")
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
		source, err := schemagen.PackageSource("generated", users, memberships)
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

	source, err := schemagen.PackageSource("generated", employees)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "func (t EmployeesTable) Manager() EmployeesTableManagerRelation")
	require.Contains(t, text, "func (t EmployeesTable) Employees() EmployeesTableEmployeesRelation")
	require.Contains(t, text, "func (r EmployeesTableEmployeesRelation) Load(ctx context.Context, db rasql.DB, parents []EmployeesRow)")
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

	source, err := schemagen.PackageSource("generated", employees)
	require.NoError(t, err)
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(generatedSelfReferentialRelationshipUsageTest), 0o600))
	// The replace directive must be absolute: t.TempDir() no longer nests the
	// scratch module under this package directory, so a relative path would
	// resolve against the temp directory's own location instead of the repo.
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))

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

	source, err := schemagen.PackageSource("generated", users, aliases)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, "func (t UsersTable) UserAs() UsersTableUserAsRelation")
	require.Contains(t, text, "func (r UsersTableUserAsRelation) Load(ctx context.Context, db rasql.DB, parents []UsersRow)")
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

	source, err := schemagen.PackageSource("generated", users, memberships)
	require.NoError(t, err)
	text := string(source)
	for _, expected := range []string{
		"func (t MembershipsTable) BillingUser() MembershipsTableBillingUserRelation",
		"func (t MembershipsTable) ShippingUser() MembershipsTableShippingUserRelation",
		"func (t UsersTable) Memberships() UsersTableMembershipsRelation",
		"func (t UsersTable) ShippingUserMemberships() UsersTableShippingUserMembershipsRelation",
		"func (r MembershipsTableBillingUserRelation) Load(ctx context.Context, db rasql.DB, children []MembershipsRow)",
		"func (r MembershipsTableShippingUserRelation) Load(ctx context.Context, db rasql.DB, children []MembershipsRow)",
	} {
		require.Contains(t, text, expected)
	}
}

// TestSchemaGeneratesDecimalColumns pins the generator's decimal mapping in
// isolation: a DecimalType column becomes a Go string field in the row type,
// and the generated descriptor literal restates precision and scale as a
// schema.DecimalType{...} value.
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

	source, err := schemagen.PackageSource("generated", invoices)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Amount\s+string$`, string(source))
	require.Regexp(t, `(?m)^\s*TaxRate\s+\*string$`, string(source))

	descriptorSource, err := schemagen.DescriptorSource("generated", invoices)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}}`)
	require.Contains(t, string(descriptorSource), `{Name: "tax_rate", Type: schema.DecimalType{Precision: 5, Scale: schema.NewDecimalScale(4)}, Nullable: true}`)
	// A stated scale of zero must survive generation: emitting nothing would
	// leave the regenerated descriptor stating no scale at all.
	require.Contains(t, string(descriptorSource), `{Name: "quantity", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(0)}}`)
}

// TestSchemaGeneratesUnsignedIntegerColumns pins the generator's signedness
// mapping. An unsigned integer column reaches 18446744073709551615, which
// int64 cannot hold, so its row field is a uint64; a signed one keeps int64.
// The generated descriptor literal restates Unsigned: true, so regenerating
// from the generated source produces the same column rather than a signed
// one.
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

	source, err := schemagen.PackageSource("generated", events)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*ID\s+uint64$`, string(source))
	require.Regexp(t, `(?m)^\s*Sequence\s+int64$`, string(source))
	require.Regexp(t, `(?m)^\s*ParentID\s+\*uint64$`, string(source))

	descriptorSource, err := schemagen.DescriptorSource("generated", events)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `{Name: "id", Type: schema.IntegerType{Unsigned: true}}`)
	require.Contains(t, string(descriptorSource), `{Name: "sequence", Type: schema.IntegerType{}}`)
	require.Contains(t, string(descriptorSource), `{Name: "parent_id", Type: schema.IntegerType{Unsigned: true}, Nullable: true}`)
}

// TestSchemaGeneratesIntegerDisplayWidthAndZeroFillColumns pins the
// generator's mapping for the two MySQL integer modifiers inspect now
// records: a stated DisplayWidth restates schema.NewIntegerDisplayWidth(n)
// in the generated descriptor literal, and a true ZeroFill restates
// ZeroFill: true, so regenerating from the generated source reproduces the
// same facts rather than silently dropping them. Neither field changes the
// generated row's Go field type: only Unsigned does that, exactly as before
// this feature existed.
func TestSchemaGeneratesIntegerDisplayWidthAndZeroFillColumns(t *testing.T) {
	counters := schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(10), ZeroFill: true}},
			{Name: "width_only", Type: schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(11)}},
		},
		PrimaryKey: []string{"id"},
	}

	descriptorSource, err := schemagen.DescriptorSource("generated", counters)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `{Name: "total", Type: schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(10), ZeroFill: true}}`)
	require.Contains(t, string(descriptorSource), `{Name: "width_only", Type: schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(11)}}`)
}

// TestSchemaGeneratesDecimalUnsignedAndZeroFillColumns pins the generator's
// mapping for the two MySQL decimal modifiers inspect now records: a true
// Unsigned restates Unsigned: true in the generated descriptor literal, and
// a true ZeroFill restates ZeroFill: true, so regenerating from the
// generated source reproduces the same facts rather than silently dropping
// them.
func TestSchemaGeneratesDecimalUnsignedAndZeroFillColumns(t *testing.T) {
	invoices := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true}},
			{Name: "unsigned_only", Type: schema.DecimalType{Precision: 8, Scale: schema.NewDecimalScale(2), Unsigned: true}},
		},
		PrimaryKey: []string{"id"},
	}

	descriptorSource, err := schemagen.DescriptorSource("generated", invoices)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true}}`)
	require.Contains(t, string(descriptorSource), `{Name: "unsigned_only", Type: schema.DecimalType{Precision: 8, Scale: schema.NewDecimalScale(2), Unsigned: true}}`)
}

// TestSchemaGeneratesGeneratedColumns pins the generator's mapping for a
// generated column: GeneratedExpression and GeneratedStorage both restate
// in the generated descriptor literal, so regenerating from the generated
// source reproduces the same generated column rather than silently turning
// it into a plain writable one. The row type still gets an ordinary field
// for it, since a generated column is read like any other column; the two
// restated fields are also what typedInsertMany and typedUpdateWithOptions
// in the root package read to leave the column out of the default INSERT
// and UPDATE column lists automatically, and to refuse an explicit
// rasql.UpdateColumns naming it outright (see TestInsertOmitsGeneratedColumn,
// TestUpdateOmitsGeneratedColumn, and TestUpdateColumnsRejectsGeneratedColumn
// in the root package), so nothing built through the typed write path ever
// writes to the generated field.
func TestSchemaGeneratesGeneratedColumns(t *testing.T) {
	measurements := schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	}

	source, err := schemagen.PackageSource("generated", measurements)
	require.NoError(t, err)
	require.Regexp(t, `(?m)^\s*Fahrenheit\s+int64$`, string(source))
	require.Contains(t, string(source), `{Name: "fahrenheit", Type: schema.IntegerType{}, GeneratedExpression: "celsius * 9 / 5 + 32", GeneratedStorage: schema.GeneratedStored}`)
}

// TestSchemaGeneratesTextWidthColumns pins the generator's text-width
// mapping. A stated width restates schema.NewTextWidth(n) in the generated
// descriptor literal, so regenerating from the generated source produces the
// same column rather than an unbounded one; an unstated width emits a plain
// schema.TextType{}, exactly as it did before TextType had a width.
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

	descriptorSource, err := schemagen.DescriptorSource("generated", users)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}}`)
	require.Contains(t, string(descriptorSource), `{Name: "bio", Type: schema.TextType{}, Nullable: true}`)
	// A stated width of zero must survive generation the same way a stated
	// decimal scale of zero does: emitting nothing would leave the
	// regenerated descriptor stating no width at all.
	require.Contains(t, string(descriptorSource), `{Name: "flag", Type: schema.TextType{Width: schema.NewTextWidth(0)}}`)
}

// TestSchemaGeneratesFixedWidthTextColumns pins the generator's Fixed
// mapping, the counterpart to TestSchemaGeneratesTextWidthColumns: a
// fixed-width column restates Fixed: true alongside Width in the generated
// descriptor literal, so regenerating from the generated source keeps
// rendering CHAR(n) rather than silently reverting to VARCHAR(n) and
// reintroducing the diff this package's Fixed support fixes.
func TestSchemaGeneratesFixedWidthTextColumns(t *testing.T) {
	events := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "code", Type: schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}},
		},
		PrimaryKey: []string{"id"},
	}

	descriptorSource, err := schemagen.DescriptorSource("generated", events)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `{Name: "code", Type: schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}}`)
}

func TestSchemaRejectsInvalidPackageName(t *testing.T) {
	_, err := schemagen.PackageSource("not-valid")
	require.Error(t, err)
}

func TestSchemaRejectsReservedColumnFieldName(t *testing.T) {
	for _, columnName := range []string{"table", "as", "ref", "column", "scan_row", "scan_destinations", "column_value"} {
		t.Run(columnName, func(t *testing.T) {
			_, err := schemagen.PackageSource("generated", schema.TableDef{
				Name: "users",
				Columns: []schema.ColumnDef{
					{Name: "id", Type: schema.IntegerType{}},
					{Name: columnName, Type: schema.TextType{}},
				},
				PrimaryKey: []string{"id"},
			})
			require.ErrorContains(t, err, "reserved generated method")
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

	_, err := schemagen.PackageSource("generated", users, orders)
	require.ErrorContains(t, err, `relationships[0] "BillingUser"`)
	require.ErrorContains(t, err, `relationships[1] "billing_user"`)
	require.ErrorContains(t, err, `duplicate generated method "BillingUser"`)
}

func TestSchemaAllowsScanColumns(t *testing.T) {
	source, err := schemagen.PackageSource("generated", schema.TableDef{
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
	_, err := schemagen.PackageSource("generated",
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

	_, err := schemagen.PackageSource("generated", users, orders, collision)
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

	err := schemagen.Validate("generated", users, orders)
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

	source, err := schemagen.PackageSource("generated", users, orders)
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

	source, err := schemagen.PackageSource("generated", apiKeys)
	require.NoError(t, err)
	require.Contains(t, string(source), "func APIKeys() APIKeysTable {")
	require.Contains(t, string(source), "type APIKeysRow struct {")

	// The api_key column must spell "API" the same way the api_keys table
	// does, so the package does not expose the same word two ways.
	require.Contains(t, string(source), "\tAPIKey string\n")

	descriptorSource, err := schemagen.DescriptorSource("generated", apiKeys)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), "var apiKeysDef = schema.TableDef{")
	require.Contains(t, string(descriptorSource), "var apiKeysTable = APIKeysTable{rasql.TableFrom[APIKeysRow](apiKeysDef)}")
	require.Contains(t, string(descriptorSource), "func APIKeysDef() schema.TableDef { return apiKeysDef.Clone() }")
}

func TestSchemaRejectsCollidingInitialismNames(t *testing.T) {
	_, err := schemagen.PackageSource("generated",
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

// TestDescriptorSourceStatesEveryOptionKind checks that the literal writer
// covers every field the option form used to fold into a constructor call:
// a named schema, unique constraints named and unnamed, checks named and
// unnamed, an exclusion constraint with a non-default method, several
// elements, a predicate, and a deferrable clause, a plain and a unique
// index, a single-column and a composite foreign key with OnDelete and
// OnUpdate, and a relationship. Compilability
// of a definition with foreign keys and relationships is already pinned by
// TestSchemaIsDeterministicAndCompiles and TestSchemaGeneratesTypedRelationships;
// this test pins the literal text those constructs produce.
func TestDescriptorSourceStatesEveryOptionKind(t *testing.T) {
	widgets := schema.TableDef{
		Schema: "app",
		Name:   "widgets",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "code", Type: schema.TextType{Width: schema.NewTextWidth(10), Fixed: true}},
			{Name: "bio", Type: schema.TextType{}, Nullable: true},
			{Name: "price", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "owner_id", Type: schema.IntegerType{}, Nullable: true},
			{Name: "combo_a", Type: schema.IntegerType{}, Nullable: true},
			{Name: "combo_b", Type: schema.IntegerType{}, Nullable: true},
			{Name: "status", Type: schema.TextType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey:              []string{"id"},
		Strict:                  true,
		WithoutRowID:            true,
		PrimaryKeyAutoincrement: true,
		PrimaryKeyOnConflict:    schema.ConflictReplace,
		UniqueConstraints: []schema.UniqueDef{
			{Name: "uq_code", Columns: []string{"code"}},
			{Columns: []string{"bio"}},
			{
				Name:             "uq_price_owner",
				Columns:          []string{"price"},
				Deferrable:       schema.DeferrableInitiallyDeferred,
				NullsNotDistinct: true,
				IncludeColumns:   []string{"owner_id"},
				OnConflict:       schema.ConflictReplace,
			},
			{
				Name:              "uq_status_temporal",
				Columns:           []string{"status"},
				Temporal:          true,
				StorageParameters: map[string]string{"fillfactor": "70"},
				Tablespace:        "pg_custom",
				ReplicaIdentity:   true,
				Collations:        map[string]string{"status": "C"},
			},
		},
		Checks: []schema.CheckDef{
			{Name: "chk_price", Expression: "price >= 0", NoInherit: true, NotValid: true, NotEnforced: true},
			{Expression: "id > 0"},
		},
		ExclusionConstraints: []schema.ExclusionDef{
			{
				Name:   "excl_owner_combo",
				Method: "gist",
				Elements: []schema.ExclusionElementDef{
					{Expression: "owner_id", Operator: "="},
					{Expression: "combo_a", Operator: "<>"},
				},
				Predicate:  "owner_id IS NOT NULL",
				Deferrable: schema.DeferrableInitiallyDeferred,
			},
		},
		Indexes: []schema.IndexDef{
			{Name: "idx_owner", Columns: []string{"owner_id"}},
			{Name: "uidx_code", Columns: []string{"code"}, Unique: true},
			{Name: "idx_bio_gin", Columns: []string{"bio"}, Method: "gin"},
			{Name: "idx_price_active", Columns: []string{"price"}, Predicate: "price > 0"},
			{Name: "idx_lower_bio", Expressions: []string{"lower(bio)"}},
			{Name: "idx_price_status", Columns: []string{"price"}, IncludeColumns: []string{"status"}, Invisible: true},
			{Name: "idx_created_at_desc", Keys: []schema.IndexKeyDef{{Expression: "created_at", Descending: true, Collation: "C", OperatorClass: "text_pattern_ops", PrefixLength: 8}}},
			{Name: "idx_status_invalid", Columns: []string{"status"}, NotValid: true, StorageParameters: map[string]string{"fillfactor": "70"}, Tablespace: "pg_custom"},
			{Name: "uidx_status_replident", Columns: []string{"status"}, Unique: true, ReplicaIdentity: true},
			{Name: "uidx_created_at_nulls", Columns: []string{"created_at"}, Unique: true, NullsNotDistinct: true},
			{Name: "idx_bio_nulls_first", Keys: []schema.IndexKeyDef{{Expression: "bio", NullsOrder: schema.NullsFirst}}},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name:              "fk_owner",
				Columns:           []string{"owner_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
				Match:             schema.MatchFull,
				OnDelete:          schema.Cascade,
				OnUpdate:          schema.Restrict,
				Deferrable:        schema.DeferrableInitiallyDeferred,
				NotValid:          true,
				NotEnforced:       true,
			},
			{
				Columns:           []string{"combo_a", "combo_b"},
				ReferencedSchema:  "app",
				ReferencedTable:   "combos",
				ReferencedColumns: []string{"a", "b"},
			},
			{
				Name:              "fk_owner_temporal",
				Columns:           []string{"owner_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
				OnDelete:          schema.SetNull,
				Temporal:          true,
				DeleteSetColumns:  []string{"owner_id"},
			},
		},
		Relationships: []schema.RelationshipDef{
			{
				Name:              "Owner",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"owner_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
			},
			{
				Name:              "Combo",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"combo_a", "combo_b"},
				ReferencedSchema:  "app",
				ReferencedTable:   "combos",
				ReferencedColumns: []string{"a", "b"},
			},
		},
	}
	require.NoError(t, widgets.Validate())

	source, err := schemagen.DescriptorSource("generated", widgets)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, `Schema: "app"`)
	require.Contains(t, text, "Strict:                  true,\n")
	require.Contains(t, text, "WithoutRowID:            true,\n")
	require.Contains(t, text, "PrimaryKeyAutoincrement: true,\n")
	require.Contains(t, text, "PrimaryKeyOnConflict:    schema.ConflictReplace,\n")
	require.Contains(t, text, `{Name: "uq_code", Columns: []string{"code"}}`)
	require.Contains(t, text, `{Columns: []string{"bio"}}`)
	require.Contains(t, text, `{Name: "uq_price_owner", Columns: []string{"price"}, Deferrable: schema.DeferrableInitiallyDeferred, NullsNotDistinct: true, IncludeColumns: []string{"owner_id"}, OnConflict: schema.ConflictReplace}`)
	require.Contains(t, text, `{Name: "uq_status_temporal", Columns: []string{"status"}, Temporal: true, StorageParameters: map[string]string{"fillfactor": "70"}, Tablespace: "pg_custom", ReplicaIdentity: true, Collations: map[string]string{"status": "C"}}`)
	require.Contains(t, text, `{Name: "chk_price", Expression: "price >= 0", NoInherit: true, NotValid: true, NotEnforced: true}`)
	require.Contains(t, text, `{Expression: "id > 0"}`)
	require.Contains(t, text, `{Name: "excl_owner_combo", Method: schema.IndexMethod("gist"), Elements: []schema.ExclusionElementDef{`)
	require.Contains(t, text, `{Expression: "owner_id", Operator: "="}`)
	require.Contains(t, text, `{Expression: "combo_a", Operator: "<>"}`)
	require.Contains(t, text, `Predicate: "owner_id IS NOT NULL", Deferrable: schema.DeferrableInitiallyDeferred}`)
	require.Contains(t, text, `{Name: "idx_owner", Columns: []string{"owner_id"}}`)
	require.Contains(t, text, `{Name: "uidx_code", Columns: []string{"code"}, Unique: true}`)
	require.Contains(t, text, `{Name: "idx_bio_gin", Columns: []string{"bio"}, Method: schema.IndexMethod("gin")}`)
	require.Contains(t, text, `{Name: "idx_price_active", Columns: []string{"price"}, Predicate: "price > 0"}`)
	require.Contains(t, text, `{Name: "idx_lower_bio", Expressions: []string{"lower(bio)"}}`)
	require.Contains(t, text, `{Name: "idx_price_status", Columns: []string{"price"}, IncludeColumns: []string{"status"}, Invisible: true}`)
	require.Contains(t, text, `{Name: "idx_created_at_desc", Keys: []schema.IndexKeyDef{`)
	require.Contains(t, text, `{Expression: "created_at", Descending: true, Collation: "C", OperatorClass: "text_pattern_ops", PrefixLength: 8}`)
	require.Contains(t, text, `{Name: "idx_status_invalid", Columns: []string{"status"}, NotValid: true, StorageParameters: map[string]string{"fillfactor": "70"}, Tablespace: "pg_custom"}`)
	require.Contains(t, text, `{Name: "uidx_status_replident", Columns: []string{"status"}, Unique: true, ReplicaIdentity: true}`)
	require.Contains(t, text, `{Name: "uidx_created_at_nulls", Columns: []string{"created_at"}, Unique: true, NullsNotDistinct: true}`)
	require.Contains(t, text, `{Name: "idx_bio_nulls_first", Keys: []schema.IndexKeyDef{`)
	require.Contains(t, text, `{Expression: "bio", NullsOrder: schema.NullsFirst}`)
	require.Contains(t, text, `{Name: "fk_owner", Columns: []string{"owner_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}, Match: schema.MatchFull, OnDelete: schema.Cascade, OnUpdate: schema.Restrict, Deferrable: schema.DeferrableInitiallyDeferred, NotValid: true, NotEnforced: true}`)
	require.Contains(t, text, `{Columns: []string{"combo_a", "combo_b"}, ReferencedSchema: "app", ReferencedTable: "combos", ReferencedColumns: []string{"a", "b"}}`)
	require.Contains(t, text, `{Name: "fk_owner_temporal", Columns: []string{"owner_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}, OnDelete: schema.SetNull, Temporal: true, DeleteSetColumns: []string{"owner_id"}}`)
	require.Contains(t, text, `{Name: "Owner", Kind: schema.RelationshipBelongsTo, Columns: []string{"owner_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}}`)
	require.Contains(t, text, `{Name: "Combo", Kind: schema.RelationshipBelongsTo, Columns: []string{"combo_a", "combo_b"}, ReferencedSchema: "app", ReferencedTable: "combos", ReferencedColumns: []string{"a", "b"}}`)
}

// TestDescriptorSourceStatesVirtualTableFacts proves that VirtualTableModule,
// VirtualTableModuleArguments, and a hidden column all reach the generated
// literal.
func TestDescriptorSourceStatesVirtualTableFacts(t *testing.T) {
	postsFTS := schema.TableDef{
		Name: "posts_fts",
		Columns: []schema.ColumnDef{
			{Name: "body", Type: schema.TextType{}, Nullable: true},
			{Name: "posts_fts", Type: schema.TextType{}, Nullable: true, Hidden: true},
			{Name: "rank", Type: schema.TextType{}, Nullable: true, Hidden: true},
		},
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"body", "tokenize='porter'"},
	}
	require.NoError(t, postsFTS.Validate())

	source, err := schemagen.DescriptorSource("generated", postsFTS)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, `VirtualTableModule:          "fts5",`)
	require.Contains(t, text, `VirtualTableModuleArguments: []string{"body", "tokenize='porter'"},`)
	require.Contains(t, text, `{Name: "posts_fts", Type: schema.TextType{}, Nullable: true, Hidden: true}`)
}

// TestDescriptorSourceStatesUniqueConstraintKeys proves that a UniqueDef
// naming Keys instead of Columns reaches the generated literal, reusing
// writeIndexKeyDefLiteral the same way IndexDef.Keys already does.
func TestDescriptorSourceStatesUniqueConstraintKeys(t *testing.T) {
	members := schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{Keys: []schema.IndexKeyDef{{Expression: "email", Descending: true, Collation: "nocase"}}},
		},
	}
	require.NoError(t, members.Validate())

	source, err := schemagen.DescriptorSource("generated", members)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, `Keys: []schema.IndexKeyDef{`)
	require.Contains(t, text, `{Expression: "email", Descending: true, Collation: "nocase"}`)
}

// TestDescriptorSourceKeepsEveryMatchingRelationship pins the case where the
// old option form lost information: two relationships that both match one
// foreign key. writeForeignKeyOptions's RelationshipNamed option could carry
// only one name, so matchingRelationshipName kept the first and silently
// dropped the second. The literal writer states table.Relationships
// directly, so both survive.
func TestDescriptorSourceKeepsEveryMatchingRelationship(t *testing.T) {
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
				Name:              "Buyer",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"user_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
			},
			{
				Name:              "Account",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"user_id"},
				ReferencedTable:   "users",
				ReferencedColumns: []string{"id"},
			},
		},
	}

	source, err := schemagen.DescriptorSource("generated", users, orders)
	require.NoError(t, err)
	text := string(source)
	require.Contains(t, text, `Name: "Buyer"`)
	require.Contains(t, text, `Name: "Account"`)
}

// TestDescriptorTestSourceFailsAnEditedDefinition pins the promise that
// hand-editing a generated definition to be invalid fails in the caller's
// own test run rather than surfacing only against a database.
func TestDescriptorTestSourceFailsAnEditedDefinition(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	tableSource, err := schemagen.TableSurfaceSource("generated", users, users)
	require.NoError(t, err)
	descriptorSource, err := schemagen.DescriptorSource("generated", users)
	require.NoError(t, err)
	descriptorTestSource, err := schemagen.DescriptorTestSource("generated", users)
	require.NoError(t, err)

	// Corrupt the descriptor after generation, the same way a hand-edit of a
	// DO-NOT-EDIT file would: blank the table name, which Validate rejects
	// as an invalid identifier.
	corrupted := strings.Replace(string(descriptorSource), `Name: "users",`, `Name: "",`, 1)
	require.NotEqual(t, string(descriptorSource), corrupted, "the replacement must actually find something to corrupt")

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "users_gen.go"), tableSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen.go"), []byte(corrupted), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen_test.go"), descriptorTestSource, 0o600))
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.Errorf(t, err, "corrupted descriptor passed the generated test:\n%s", output)
	require.Contains(t, string(output), "TestRasqlgenGeneratedDefinitionsAreValid")
}

// handWrittenDefinitionsTest is a test file somebody wrote by hand under the
// name the generated test used to take. It is the name that mattered: a
// generated test declaring it too would redeclare it in the same package and
// stop the package compiling, and nothing in rasqlgen would notice, because
// rasqlgen inspects only the path it writes and this declaration lives
// somewhere else.
const handWrittenDefinitionsTest = `package generated

import "testing"

func TestGeneratedDefinitionsAreValid(t *testing.T) {
	t.Log("written by hand, and here first")
}
`

// TestDescriptorTestSourceAvoidsHandWrittenNameCollision pins that the
// generated test can be written into a package that already declares a test
// under the generator's former name, and that the package still builds and
// runs both tests.
func TestDescriptorTestSourceAvoidsHandWrittenNameCollision(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	tableSource, err := schemagen.TableSurfaceSource("generated", users, users)
	require.NoError(t, err)
	descriptorSource, err := schemagen.DescriptorSource("generated", users)
	require.NoError(t, err)
	descriptorTestSource, err := schemagen.DescriptorTestSource("generated", users)
	require.NoError(t, err)
	require.NotContains(t, string(descriptorTestSource), "func TestGeneratedDefinitionsAreValid(",
		"the generated test must not take a name a person could reasonably write")

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "users_gen.go"), tableSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen.go"), descriptorSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen_test.go"), descriptorTestSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "hand_written_test.go"), []byte(handWrittenDefinitionsTest), 0o600))
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(repository)+"\n"), 0o600))

	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "go mod tidy output:\n%s", output)

	command = exec.CommandContext(t.Context(), "go", "test", "-run", "DefinitionsAreValid", "-v", ".")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoErrorf(t, err, "go test output:\n%s", output)
	require.Contains(t, string(output), "TestGeneratedDefinitionsAreValid", "go test output:\n%s", output)
	require.Contains(t, string(output), "TestRasqlgenGeneratedDefinitionsAreValid", "go test output:\n%s", output)
}

// TestGenerationIsIdempotent pins that generating twice from the same input
// produces identical bytes in all three files, not just PackageSource. A
// table carrying RowName is included so a stated row name does not become a
// source of nondeterminism the plain default hides.
func TestGenerationIsIdempotent(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		RowName:    "User",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	firstTable, err := schemagen.TableSource("generated", users, users)
	require.NoError(t, err)
	secondTable, err := schemagen.TableSource("generated", users, users)
	require.NoError(t, err)
	require.Equal(t, string(firstTable), string(secondTable))

	firstSurface, err := schemagen.TableSurfaceSource("generated", users, users)
	require.NoError(t, err)
	secondSurface, err := schemagen.TableSurfaceSource("generated", users, users)
	require.NoError(t, err)
	require.Equal(t, string(firstSurface), string(secondSurface))

	firstDescriptor, err := schemagen.DescriptorSource("generated", users)
	require.NoError(t, err)
	secondDescriptor, err := schemagen.DescriptorSource("generated", users)
	require.NoError(t, err)
	require.Equal(t, string(firstDescriptor), string(secondDescriptor))

	firstTest, err := schemagen.DescriptorTestSource("generated", users)
	require.NoError(t, err)
	secondTest, err := schemagen.DescriptorTestSource("generated", users)
	require.NoError(t, err)
	require.Equal(t, string(firstTest), string(secondTest))
}

// TestSchemaRejectsDefinitionAccessorCollision pins correction 5: the
// generated exported accessor UsersDef collides with the accessor a table
// literally named users_def generates, so validateVariableNames must
// register both.
func TestSchemaRejectsDefinitionAccessorCollision(t *testing.T) {
	_, err := schemagen.PackageSource("generated",
		schema.TableDef{
			Name:       "users",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
		schema.TableDef{
			Name:       "users_def",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		},
	)
	require.ErrorContains(t, err, `duplicates generated name "UsersDef"`)
}

// renamedRowTypeUsageTest exercises the row type RowNamed renames from
// inside the temporary module: it is constructed directly, the way a real
// caller of RowNamed would write it, and passed through both directions of
// a relationship Load, so a defect in the rename reaching the relationship
// signatures fails to compile rather than only mismatching text.
const renamedRowTypeUsageTest = `package generated_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"example.com/generated"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
)

type recordingHandle struct {
	query string
}

func (h *recordingHandle) QueryContext(_ context.Context, statement string, _ ...any) (*sql.Rows, error) {
	h.query = statement
	return nil, errors.New("query recorded")
}

func (h *recordingHandle) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, errors.New("exec not supported")
}

func TestRenamedRowTypeCompilesAndLoads(t *testing.T) {
	handle := &recordingHandle{}
	db, err := rasql.New(handle, dialect.PostgreSQL())
	require.NoError(t, err)

	// The renamed row type appears directly in caller code, exactly the way
	// RowNamed exists to let store.User read better than store.UsersRow.
	user := generated.User{ID: 7}
	require.Equal(t, int64(7), user.ID)

	users := generated.Users()
	orders := generated.Orders()

	belongsTo := orders.User()
	_, err = belongsTo.Load(t.Context(), db, []generated.OrdersRow{{UserID: 7}})
	require.ErrorContains(t, err, "query recorded")

	hasMany := users.Orders()
	_, err = hasMany.Load(t.Context(), db, []generated.User{user})
	require.ErrorContains(t, err, "query recorded")
}
`

// TestSchemaRenamesRowType covers schema.RowNamed end to end: a users table
// states RowName "User" and an orders table carries a foreign key to it, so
// both relationship directions are generated. PackageSource, DescriptorSource
// and DescriptorTestSource output are checked by text, and the package is
// then compiled and run in a temporary module, because a text assertion
// alone cannot catch a relationship signature that names the renamed type on
// one side and the default on the other.
func TestSchemaRenamesRowType(t *testing.T) {
	users := schema.TableDef{
		Name:       "users",
		RowName:    "User",
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

	source, err := schemagen.PackageSource("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(source), "type User struct")
	require.Contains(t, string(source), "type UsersTable struct {\n\trasql.Table[User]\n}\n")
	require.Contains(t, string(source), "func (r *User) ScanRow(src rasql.ScanSource) error {")
	require.Contains(t, string(source), "func (r *User) ScanDestinations(columns []string) ([]any, error) {")
	require.Contains(t, string(source), "func (r User) ColumnValue(name string) (any, bool) {")
	// The untouched default: orders never sets RowName.
	require.Contains(t, string(source), "type OrdersRow struct")
	require.NotContains(t, string(source), "type UsersRow struct")
	// Has-many, on UsersTable.Orders(): the parent is the renamed row type,
	// the child stays the default.
	require.Contains(t, string(source), "parents []User) (map[int64][]OrdersRow, error)")
	// Belongs-to, on OrdersTable.User(): the child stays the default, the
	// parent is the renamed row type.
	require.Contains(t, string(source), "children []OrdersRow) (map[int64]User, error)")

	descriptorSource, err := schemagen.DescriptorSource("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), `RowName: "User"`)
	require.Contains(t, string(descriptorSource), "rasql.TableFrom[User](usersDef)")
	// orders never sets RowName, so its literal states no RowName field at all.
	require.NotContains(t, string(descriptorSource), `RowName: "OrdersRow"`)

	// DescriptorTestSource's shape does not change: it names definition
	// variables, never a row type, so RowNamed leaves it untouched.
	descriptorTestSource, err := schemagen.DescriptorTestSource("generated", users, orders)
	require.NoError(t, err)
	require.Contains(t, string(descriptorTestSource), "func TestRasqlgenGeneratedDefinitionsAreValid(t *testing.T) {")
	require.Contains(t, string(descriptorTestSource), "[]schema.TableDef{ordersDef, usersDef}")
	require.NotContains(t, string(descriptorTestSource), "RowName")
	require.NotContains(t, string(descriptorTestSource), "User")

	directory := t.TempDir()
	// PackageSource already carries every descriptor, so DescriptorSource's
	// output cannot be written beside it: the two are alternatives, not a
	// pair. Its text is asserted above instead, and the generated descriptor
	// test still compiles against the definitions PackageSource declares.
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen_test.go"), descriptorTestSource, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_usage_test.go"), []byte(renamedRowTypeUsageTest), 0o600))
	repository, err := filepath.Abs("../..")
	require.NoError(t, err)
	module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(repository) + "\n"
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

// TestSchemaRowNamedRejections covers every way a stated RowName can be
// rejected: syntactically invalid, an unexported name, a name reserved
// for a generated method, a name colliding with this table's own generated
// accessor/type/definition names, a name colliding with another table's row
// type, and a name colliding with a relationship type name.
func TestSchemaRowNamedRejections(t *testing.T) {
	baseUsers := func(rowName string) schema.TableDef {
		return schema.TableDef{
			Name:       "users",
			RowName:    rowName,
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"id"},
		}
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
	collidingRelationshipType := schema.TableDef{
		Name:       "users_table_orders_relation",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	tests := map[string]struct {
		tables  []schema.TableDef
		wantErr string
	}{
		"not a Go identifier": {
			tables:  []schema.TableDef{baseUsers("1Bad")},
			wantErr: "table.row_name",
		},
		"unexported": {
			tables:  []schema.TableDef{baseUsers("user")},
			wantErr: "table.row_name",
		},
		"reserved generated name": {
			tables:  []schema.TableDef{baseUsers("ScanRow")},
			wantErr: `RowNamed, which is a reserved generated name`,
		},
		"collides with own accessor": {
			tables:  []schema.TableDef{baseUsers("Users")},
			wantErr: `RowNamed, which collides with its own generated accessor`,
		},
		"collides with own table type": {
			tables:  []schema.TableDef{baseUsers("UsersTable")},
			wantErr: `RowNamed, which collides with its own generated table type`,
		},
		"collides with own definition accessor": {
			tables:  []schema.TableDef{baseUsers("UsersDef")},
			wantErr: `RowNamed, which collides with its own generated definition accessor`,
		},
		"collides with another table's row type": {
			tables:  []schema.TableDef{baseUsers("OrdersRow"), orders},
			wantErr: `duplicates generated name "OrdersRow"`,
		},
		"collides with a relationship type name": {
			tables:  []schema.TableDef{baseUsers("UsersTableOrdersRelation"), orders, collidingRelationshipType},
			wantErr: `duplicates generated name "UsersTableOrdersRelation"`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := schemagen.PackageSource("generated", test.tables...)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// TestSchemaRejectsGeneratedTestNameCollision pins that the fixed function
// name rasqlgen writes into schema_gen_test.go takes part in the collision
// check like every derived name does. A table named
// test_rasqlgen_generated_definitions_are_valid derives exactly that
// identifier for its accessor, so without the reservation rasqlgen would
// write both declarations itself and the package would fail to build under
// go test while still passing go build.
func TestSchemaRejectsGeneratedTestNameCollision(t *testing.T) {
	const reservedTable = "test_rasqlgen_generated_definitions_are_valid"
	reserved := schema.TableDef{
		Name:       reservedTable,
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	nearMiss := schema.TableDef{
		Name:       "test_rasqlgen_generated_definitions",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	// The two sides of the collision: the name the generated test declares,
	// and the accessor the table name derives. Renaming either without the
	// other fails here rather than in a caller's build.
	generatedTest, err := schemagen.DescriptorTestSource("generated", users)
	require.NoError(t, err)
	require.Contains(t, string(generatedTest), "func TestRasqlgenGeneratedDefinitionsAreValid(t *testing.T) {")

	for name, generate := range map[string]func(tables ...schema.TableDef) error{
		"Validate": func(tables ...schema.TableDef) error {
			return schemagen.Validate("generated", tables...)
		},
		"PackageSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.PackageSource("generated", tables...)
			return err
		},
		"TableSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.TableSource("generated", tables[0], tables...)
			return err
		},
		"TableSurfaceSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.TableSurfaceSource("generated", tables[0], tables...)
			return err
		},
		"DescriptorSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.DescriptorSource("generated", tables...)
			return err
		},
		"DescriptorTestSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.DescriptorTestSource("generated", tables...)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := generate(reserved)
			require.ErrorContains(t, err, `table "`+reservedTable+`"`)
			require.ErrorContains(t, err, `duplicates generated name "TestRasqlgenGeneratedDefinitionsAreValid"`)
			require.ErrorContains(t, err, "rasqlgen reserves")

			// The reservation must cost nothing to an ordinary table. The
			// same entry point still accepts a plain name and a near miss
			// whose accessor is a prefix of the reserved identifier.
			require.NoError(t, generate(users))
			require.NoError(t, generate(nearMiss))
		})
	}
}

// TestSchemaRejectsTablesFuncNameCollision proves that a table named
// "tables" is refused rather than silently producing a package that fails
// to build: variableName lowercases nothing off a name that is already one
// capitalized word, so table "tables" generates the package-level accessor
// Tables, which collides with the Tables function descriptorSource always
// declares in schema_gen.go. The check runs the same way
// TestSchemaRejectsGeneratedTestNameCollision does, across every entry
// point that shares validateVariableNames, since the collision is a
// package-level Go identifier and every entry point declares into the same
// namespace.
func TestSchemaRejectsTablesFuncNameCollision(t *testing.T) {
	reserved := schema.TableDef{
		Name:       "tables",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	users := schema.TableDef{
		Name:       "users",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	// TablesDef is definitionAccessorName("tables"): the descriptor
	// accessor a table named "tables" also generates. It always ends in
	// "Def", so it can never spell Tables itself, and must keep working.
	definitionAccessor := schema.TableDef{
		Name:       "tables_def",
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}

	descriptorSource, err := schemagen.DescriptorSource("generated", users)
	require.NoError(t, err)
	require.Contains(t, string(descriptorSource), "func Tables() []schema.TableDef {")

	for name, generate := range map[string]func(tables ...schema.TableDef) error{
		"Validate": func(tables ...schema.TableDef) error {
			return schemagen.Validate("generated", tables...)
		},
		"PackageSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.PackageSource("generated", tables...)
			return err
		},
		"TableSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.TableSource("generated", tables[0], tables...)
			return err
		},
		"TableSurfaceSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.TableSurfaceSource("generated", tables[0], tables...)
			return err
		},
		"DescriptorSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.DescriptorSource("generated", tables...)
			return err
		},
		"DescriptorTestSource": func(tables ...schema.TableDef) error {
			_, err := schemagen.DescriptorTestSource("generated", tables...)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := generate(reserved)
			require.ErrorContains(t, err, `table "tables"`)
			require.ErrorContains(t, err, `duplicates generated name "Tables"`)
			require.ErrorContains(t, err, "rasqlgen reserves")

			// The reservation must cost nothing to an ordinary table, or to
			// the unrelated accessor a table named "tables_def" generates.
			require.NoError(t, generate(users))
			require.NoError(t, generate(definitionAccessor))
		})
	}
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
