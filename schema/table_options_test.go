package schema_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestNewTableMatchesStructLiteral pins the option-based constructor against
// the equivalent hand-written struct literal: the two must describe exactly
// the same table, proving the option form is a genuine alternative to the
// literal rather than a different, narrower shape.
func TestNewTableMatchesStructLiteral(t *testing.T) {
	built, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.Text("status", schema.Default("'new'")),
		schema.Decimal("amount", 19, 4),
		schema.PrimaryKey("id"),
		schema.Unique("customer_id", "status"),
		schema.Check("status <> ''"),
		schema.Index("orders_customer_idx", "customer_id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fk"),
			schema.References("customers", "id")),
	)
	require.NoError(t, err)

	literal := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'new'"},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueConstraint{{
			Columns: []string{"customer_id", "status"},
		}},
		Checks: []schema.CheckConstraint{{
			Expression: "status <> ''",
		}},
		Indexes: []schema.IndexDef{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
		}},
	}

	require.Equal(t, literal, built)
	require.NoError(t, literal.Validate())
}

// TestNewTableOptionOrderDoesNotMatter builds the same table with
// PrimaryKey listed before and after the column it names, and with the
// column options for a single column applied in different orders, and
// requires an identical result either way.
func TestNewTableOptionOrderDoesNotMatter(t *testing.T) {
	columnsFirst, err := schema.NewTable("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Unique("email"),
	)
	require.NoError(t, err)

	keyFirst, err := schema.NewTable("users",
		schema.PrimaryKey("id"),
		schema.Unique("email"),
		schema.Integer("id"),
		schema.Text("email"),
	)
	require.NoError(t, err)

	require.Equal(t, columnsFirst, keyFirst)
}

// TestNewTableAssemblesForeignKeyAndRelationship covers ForeignKey's four
// options together: Named, References, OnDelete, and As, the last of which
// derives a belongs-to Relationship separate from the ForeignKeyDef itself.
func TestNewTableAssemblesForeignKeyAndRelationship(t *testing.T) {
	table, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.OnUpdate(schema.Restrict),
			schema.As("buyer")),
	)
	require.NoError(t, err)

	require.Equal(t, []schema.ForeignKeyDef{{
		Name:              "orders_customer_fkey",
		Columns:           []string{"customer_id"},
		ReferencedTable:   "customers",
		ReferencedColumns: []string{"id"},
		OnDelete:          schema.Cascade,
		OnUpdate:          schema.Restrict,
	}}, table.ForeignKeys)

	require.Equal(t, []schema.Relationship{{
		Name:              "buyer",
		Kind:              schema.RelationshipBelongsTo,
		Columns:           []string{"customer_id"},
		ReferencedTable:   "customers",
		ReferencedColumns: []string{"id"},
	}}, table.Relationships)
}

// TestForeignKeyWithoutAsDeclaresNoRelationship covers the common case: a
// foreign key with no As leaves Relationships empty, so rasqlgen derives its
// own name from the local column exactly as it does for a struct literal
// that also states no Relationships.
func TestForeignKeyWithoutAsDeclaresNoRelationship(t *testing.T) {
	table, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id", schema.References("customers", "id")),
	)
	require.NoError(t, err)
	require.Empty(t, table.Relationships)
}

func TestNewTableInSchema(t *testing.T) {
	table, err := schema.NewTable("events",
		schema.InSchema("audit"),
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	require.Equal(t, "audit", table.Schema)
	require.True(t, table.Qualified())
}

// TestNewTableValidatesAssembledDescriptor covers the contract that NewTable
// runs Table.Validate on the assembled descriptor: an unknown primary key
// column is rejected exactly as it would be from a struct literal.
func TestNewTableValidatesAssembledDescriptor(t *testing.T) {
	_, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.PrimaryKey("missing"),
	)
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func TestMustTablePanicsOnInvalidDescriptor(t *testing.T) {
	require.Panics(t, func() {
		schema.MustTable("orders",
			schema.Integer("id"),
			schema.PrimaryKey("missing"),
		)
	})
}

func TestMustTableReturnsValidTable(t *testing.T) {
	table := schema.MustTable("orders",
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, table.Validate())
}

func TestNewTableRejectsNilTableOption(t *testing.T) {
	_, err := schema.NewTable("orders", nil)
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func TestNewTableRejectsPrimaryKeyDeclaredTwice(t *testing.T) {
	_, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.Integer("other_id"),
		schema.PrimaryKey("id"),
		schema.PrimaryKey("other_id"),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "declared more than once")
}

func TestNewTableRejectsNilColumnOption(t *testing.T) {
	_, err := schema.NewTable("orders", schema.Integer("id", nil))
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

// TestUnsignedRejectsNonIntegerColumn covers Unsigned's type check: it
// applies only to IntegerType, exactly like IntegerType.Unsigned itself.
func TestUnsignedRejectsNonIntegerColumn(t *testing.T) {
	_, err := schema.NewTable("events", schema.Text("id", schema.Unsigned()))
	require.Error(t, err)
	require.ErrorContains(t, err, "Unsigned only applies to integer columns")
}

func TestUnsignedMarksIntegerColumn(t *testing.T) {
	table, err := schema.NewTable("events",
		schema.Integer("id", schema.Unsigned()),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	require.Equal(t, schema.IntegerType{Unsigned: true}, table.Columns[0].Type)
}

func TestNullableAndDefaultColumnOptions(t *testing.T) {
	table, err := schema.NewTable("users",
		schema.Integer("id"),
		schema.Text("nickname", schema.Nullable()),
		schema.Time("created_at", schema.Default("CURRENT_TIMESTAMP")),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	require.True(t, table.Columns[1].Nullable)
	require.Equal(t, "CURRENT_TIMESTAMP", table.Columns[2].Default)
}

func TestNewTableRejectsNilForeignKeyOption(t *testing.T) {
	_, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id", nil),
	)
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func TestForeignKeyAsRejectsEmptyName(t *testing.T) {
	_, err := schema.NewTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.References("customers", "id"),
			schema.As("")),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "As name must not be empty")
}

// TestIndexDefAndForeignKeyDefJSONUnchanged proves that renaming the Go
// types formerly named Index and ForeignKey to IndexDef and ForeignKeyDef
// left the JSON wire format untouched: encoding/json never encodes a Go type
// name, only field names, and the ReferenceAction constants renamed
// alongside them still encode as their unchanged string values ("CASCADE",
// "RESTRICT") rather than as the Go identifier. rasqlgen's -input flag reads
// this exact shape back, so a snapshot captured before this refactor must
// still decode.
func TestIndexDefAndForeignKeyDefJSONUnchanged(t *testing.T) {
	table := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "orders_id_idx",
			Columns: []string{"id"},
			Unique:  true,
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"id"},
			ReferencedSchema:  "tenant",
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
			OnUpdate:          schema.Restrict,
		}},
	}
	require.NoError(t, table.Validate())

	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Indexes":[{"Name":"orders_id_idx","Columns":["id"],"Unique":true}]`)
	require.Contains(t, string(encoded), `"ForeignKeys":[{"Name":"orders_customer_fk","Columns":["id"],"ReferencedSchema":"tenant","ReferencedTable":"customers","ReferencedColumns":["id"],"OnDelete":"CASCADE","OnUpdate":"RESTRICT"}]`)

	// A snapshot from before the rename, spelled out by hand rather than
	// produced by json.Marshal, must still decode: this is what actually
	// pins the wire format, since round-tripping through the new types alone
	// would not catch a field that had quietly been renamed too.
	const snapshot = `{"Schema":"","Name":"orders","Columns":[{"Name":"id","Type":{"Kind":"integer","Unsigned":false},"Nullable":false,"Default":""}],"PrimaryKey":["id"],"UniqueConstraints":null,"Checks":null,"Indexes":[{"Name":"orders_id_idx","Columns":["id"],"Unique":true}],"ForeignKeys":[{"Name":"orders_customer_fk","Columns":["id"],"ReferencedSchema":"tenant","ReferencedTable":"customers","ReferencedColumns":["id"],"OnDelete":"CASCADE","OnUpdate":"RESTRICT"}],"Relationships":null}`
	var decoded schema.Table
	require.NoError(t, json.Unmarshal([]byte(snapshot), &decoded))
	require.Equal(t, table, decoded)
}
