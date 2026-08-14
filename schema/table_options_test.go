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
	built, err := schema.NewTableDef("orders",
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

	literal := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'new'"},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Columns: []string{"customer_id", "status"},
		}},
		Checks: []schema.CheckDef{{
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
	columnsFirst, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.Unique("email"),
	)
	require.NoError(t, err)

	keyFirst, err := schema.NewTableDef("users",
		schema.PrimaryKey("id"),
		schema.Unique("email"),
		schema.Integer("id"),
		schema.Text("email"),
	)
	require.NoError(t, err)

	require.Equal(t, columnsFirst, keyFirst)
}

// TestNewTableAssemblesForeignKeyAndRelationship covers ForeignKey's four
// options together: Named, References, OnDelete, and RelationshipNamed, the
// last of which derives a belongs-to Relationship separate from the
// ForeignKeyDef itself.
func TestNewTableAssemblesForeignKeyAndRelationship(t *testing.T) {
	table, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.OnUpdate(schema.Restrict),
			schema.RelationshipNamed("buyer")),
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

	require.Equal(t, []schema.RelationshipDef{{
		Name:              "buyer",
		Kind:              schema.RelationshipBelongsTo,
		Columns:           []string{"customer_id"},
		ReferencedTable:   "customers",
		ReferencedColumns: []string{"id"},
	}}, table.Relationships)
}

// TestForeignKeyWithoutAsDeclaresNoRelationship covers the common case: a
// foreign key with no RelationshipNamed leaves Relationships empty, so
// rasqlgen derives its own name from the local column exactly as it does
// for a struct literal that also states no Relationships.
func TestForeignKeyWithoutAsDeclaresNoRelationship(t *testing.T) {
	table, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id", schema.References("customers", "id")),
	)
	require.NoError(t, err)
	require.Empty(t, table.Relationships)
}

func TestNewTableInSchema(t *testing.T) {
	table, err := schema.NewTableDef("events",
		schema.InSchema("audit"),
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	require.Equal(t, "audit", table.Schema)
	require.True(t, table.Qualified())
}

// TestNewTableDefValidatesAssembledDescriptor covers the contract that
// NewTableDef runs Table.Validate on the assembled descriptor: an unknown
// primary key column is rejected exactly as it would be from a struct
// literal.
func TestNewTableDefValidatesAssembledDescriptor(t *testing.T) {
	_, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.PrimaryKey("missing"),
	)
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func TestMustTablePanicsOnInvalidDescriptor(t *testing.T) {
	require.Panics(t, func() {
		schema.MustTableDef("orders",
			schema.Integer("id"),
			schema.PrimaryKey("missing"),
		)
	})
}

func TestMustTableReturnsValidTable(t *testing.T) {
	table := schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, table.Validate())
}

func TestNewTableRejectsNilTableOption(t *testing.T) {
	_, err := schema.NewTableDef("orders", nil)
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

func TestNewTableRejectsPrimaryKeyDeclaredTwice(t *testing.T) {
	_, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.Integer("other_id"),
		schema.PrimaryKey("id"),
		schema.PrimaryKey("other_id"),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "declared more than once")
}

func TestNewTableRejectsNilColumnOption(t *testing.T) {
	_, err := schema.NewTableDef("orders", schema.Integer("id", nil))
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

// TestUnsignedRejectsNonIntegerColumn covers Unsigned's type check: it
// applies only to IntegerType, exactly like IntegerType.Unsigned itself.
func TestUnsignedRejectsNonIntegerColumn(t *testing.T) {
	_, err := schema.NewTableDef("events", schema.Text("id", schema.Unsigned()))
	require.Error(t, err)
	require.ErrorContains(t, err, "Unsigned only applies to integer columns")
}

func TestUnsignedMarksIntegerColumn(t *testing.T) {
	table, err := schema.NewTableDef("events",
		schema.Integer("id", schema.Unsigned()),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	require.Equal(t, schema.IntegerType{Unsigned: true}, table.Columns[0].Type)
}

// TestWidthRejectsNonTextColumn covers Width's type check: it applies only
// to TextType, the same shape as TestUnsignedRejectsNonIntegerColumn.
func TestWidthRejectsNonTextColumn(t *testing.T) {
	_, err := schema.NewTableDef("events", schema.Integer("id", schema.Width(255)))
	require.Error(t, err)
	require.ErrorContains(t, err, "Width only applies to text columns")
}

func TestWidthMarksTextColumn(t *testing.T) {
	table, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email", schema.Width(255)),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	width, stated := table.Columns[1].Type.(schema.TextType).Width.Value()
	require.True(t, stated)
	require.Equal(t, 255, width)
}

// TestWidthAcceptsZero covers the reason schema.TextWidth exists rather than
// a plain int: a stated width of 0 (VARCHAR(0), unusual but legitimate on
// dialects that accept it) must read back as stated, not as the unstated
// zero value a bare int field could not tell apart from it.
func TestWidthAcceptsZero(t *testing.T) {
	table, err := schema.NewTableDef("flags",
		schema.Text("value", schema.Width(0)),
	)
	require.NoError(t, err)
	width, stated := table.Columns[0].Type.(schema.TextType).Width.Value()
	require.True(t, stated)
	require.Equal(t, 0, width)
}

// TestFixedRejectsNonTextColumn covers Fixed's type check: it applies only
// to TextType, the same shape as TestUnsignedRejectsNonIntegerColumn and
// TestWidthRejectsNonTextColumn.
func TestFixedRejectsNonTextColumn(t *testing.T) {
	_, err := schema.NewTableDef("events", schema.Integer("id", schema.Fixed()))
	require.Error(t, err)
	require.ErrorContains(t, err, "Fixed only applies to text columns")
}

// TestFixedMarksTextColumn covers the option applying regardless of whether
// Width was already applied, since options run in the order given and
// Fixed must not depend on Width running first.
func TestFixedMarksTextColumn(t *testing.T) {
	table, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("code", schema.Width(36), schema.Fixed()),
		schema.Text("token", schema.Fixed(), schema.Width(32)),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)
	require.True(t, table.Columns[1].Type.(schema.TextType).Fixed)
	require.True(t, table.Columns[2].Type.(schema.TextType).Fixed)
}

// TestFixedWithoutWidthRejected covers the validation Fixed exists to
// guard: bare CHAR means CHAR(1), not an unbounded column, so a fixed-width
// column that never states a width is rejected at NewTableDef, not just at
// a later Validate call.
func TestFixedWithoutWidthRejected(t *testing.T) {
	_, err := schema.NewTableDef("users", schema.Text("code", schema.Fixed()))
	require.Error(t, err)
	require.ErrorContains(t, err, "fixed-width text column must state a width")
}

func TestNullableAndDefaultColumnOptions(t *testing.T) {
	table, err := schema.NewTableDef("users",
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
	_, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id", nil),
	)
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
}

// TestRowNamedSetsRowName covers RowNamed through both NewTableDef and
// MustTableDef, that a definition without it leaves RowName empty, and that
// applying it before or after the column constructors gives the same
// descriptor.
func TestRowNamedSetsRowName(t *testing.T) {
	columnsFirst, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.RowNamed("User"),
	)
	require.NoError(t, err)
	require.Equal(t, "User", columnsFirst.RowName)

	rowNamedFirst, err := schema.NewTableDef("users",
		schema.RowNamed("User"),
		schema.Integer("id"),
	)
	require.NoError(t, err)
	require.Equal(t, columnsFirst, rowNamedFirst)

	require.Equal(t, "User", schema.MustTableDef("users",
		schema.Integer("id"),
		schema.RowNamed("User"),
	).RowName)

	withoutOption, err := schema.NewTableDef("users", schema.Integer("id"))
	require.NoError(t, err)
	require.Empty(t, withoutOption.RowName)
}

// TestRowNamedRejectsInvalidGoIdentifier proves that NewTableDef's final
// Validate call, not just RowNamed's own builder check, catches a RowName
// that is not a valid exported Go identifier.
func TestRowNamedRejectsInvalidGoIdentifier(t *testing.T) {
	tests := map[string]string{
		"unexported": "user",
		"go keyword": "type",
	}

	for name, rowName := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := schema.NewTableDef("users",
				schema.Integer("id"),
				schema.RowNamed(rowName),
			)
			require.Error(t, err)
			var validationErr *schema.ValidationError
			require.True(t, errors.As(err, &validationErr))
			require.ErrorContains(t, err, "table.row_name")
		})
	}
}

func TestRowNamedRejectsEmptyName(t *testing.T) {
	_, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.RowNamed(""),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "RowNamed name must not be empty")

	require.Panics(t, func() {
		schema.MustTableDef("users",
			schema.Integer("id"),
			schema.RowNamed(""),
		)
	})
}

func TestForeignKeyAsRejectsEmptyName(t *testing.T) {
	_, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.References("customers", "id"),
			schema.RelationshipNamed("")),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "RelationshipNamed name must not be empty")
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
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
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
	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal([]byte(snapshot), &decoded))
	require.Equal(t, table, decoded)
}

func TestUniqueNamedDeclaresNamedConstraint(t *testing.T) {
	table, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.UniqueNamed("uq_users_email", "email"),
	)
	require.NoError(t, err)
	require.Equal(t, []schema.UniqueDef{{
		Name:    "uq_users_email",
		Columns: []string{"email"},
	}}, table.UniqueConstraints)
}

func TestCheckNamedDeclaresNamedConstraint(t *testing.T) {
	table, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.CheckNamed("chk_users_email", "email <> ''"),
	)
	require.NoError(t, err)
	require.Equal(t, []schema.CheckDef{{
		Name:       "chk_users_email",
		Expression: "email <> ''",
	}}, table.Checks)
}

func TestUniqueIndexDeclaresUniqueIndex(t *testing.T) {
	table, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
		schema.UniqueIndex("users_email_idx", "email"),
	)
	require.NoError(t, err)
	require.Equal(t, []schema.IndexDef{{
		Name:    "users_email_idx",
		Columns: []string{"email"},
		Unique:  true,
	}}, table.Indexes)
}

// TestForeignKeyOnDeclaresCompositeForeignKey covers ForeignKeyOn, the
// composite counterpart to ForeignKey, sharing ForeignKey's option set
// including the new ReferencesIn for a schema-qualified target.
func TestForeignKeyOnDeclaresCompositeForeignKey(t *testing.T) {
	table, err := schema.NewTableDef("order_items",
		schema.Integer("order_id"),
		schema.Integer("tenant_id"),
		schema.PrimaryKey("order_id", "tenant_id"),
		schema.ForeignKeyOn([]string{"order_id", "tenant_id"},
			schema.Named("order_items_order_fkey"),
			schema.ReferencesIn("billing", "orders", "id", "tenant_id"),
			schema.OnDelete(schema.Cascade)),
	)
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{{
		Name:              "order_items_order_fkey",
		Columns:           []string{"order_id", "tenant_id"},
		ReferencedSchema:  "billing",
		ReferencedTable:   "orders",
		ReferencedColumns: []string{"id", "tenant_id"},
		OnDelete:          schema.Cascade,
	}}, table.ForeignKeys)
}

func TestReferencesInQualifiesSingleColumnForeignKey(t *testing.T) {
	table, err := schema.NewTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.ReferencesIn("tenant", "customers", "id")),
	)
	require.NoError(t, err)
	require.Equal(t, []schema.ForeignKeyDef{{
		Columns:           []string{"customer_id"},
		ReferencedSchema:  "tenant",
		ReferencedTable:   "customers",
		ReferencedColumns: []string{"id"},
	}}, table.ForeignKeys)
}

// TestNewTableMatchesStructLiteralEveryFeature is the equivalence test the
// first round of option-based construction omitted: it exercises every
// TableOption and ForeignKeyOption together, including the ones piece C
// added, and requires the option form and the equivalent hand-written,
// fully-keyed struct literal to describe exactly the same table.
func TestNewTableMatchesStructLiteralEveryFeature(t *testing.T) {
	built, err := schema.NewTableDef("orders",
		schema.InSchema("billing"),
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.Integer("tenant_id"),
		schema.Text("status", schema.Default("'new'")),
		schema.Text("note", schema.Nullable()),
		schema.Decimal("amount", 19, 4),
		schema.PrimaryKey("id"),
		schema.Unique("customer_id", "status"),
		schema.UniqueNamed("uq_orders_tenant_id", "tenant_id", "id"),
		schema.Check("amount >= 0"),
		schema.CheckNamed("chk_orders_status", "status <> ''"),
		schema.Index("orders_customer_idx", "customer_id"),
		schema.UniqueIndex("orders_tenant_status_idx", "tenant_id", "status"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.OnUpdate(schema.Restrict),
			schema.RelationshipNamed("customer")),
		schema.ForeignKeyOn([]string{"tenant_id", "customer_id"},
			schema.Named("orders_tenant_customer_fkey"),
			schema.ReferencesIn("crm", "tenant_customers", "tenant_id", "customer_id"),
			schema.OnDelete(schema.SetNull)),
	)
	require.NoError(t, err)

	literal := schema.TableDef{
		Schema: "billing",
		Name:   "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "tenant_id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'new'"},
			{Name: "note", Type: schema.TextType{}, Nullable: true},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{Columns: []string{"customer_id", "status"}},
			{Name: "uq_orders_tenant_id", Columns: []string{"tenant_id", "id"}},
		},
		Checks: []schema.CheckDef{
			{Expression: "amount >= 0"},
			{Name: "chk_orders_status", Expression: "status <> ''"},
		},
		Indexes: []schema.IndexDef{
			{Name: "orders_customer_idx", Columns: []string{"customer_id"}},
			{Name: "orders_tenant_status_idx", Columns: []string{"tenant_id", "status"}, Unique: true},
		},
		ForeignKeys: []schema.ForeignKeyDef{
			{
				Name:              "orders_customer_fkey",
				Columns:           []string{"customer_id"},
				ReferencedTable:   "customers",
				ReferencedColumns: []string{"id"},
				OnDelete:          schema.Cascade,
				OnUpdate:          schema.Restrict,
			},
			{
				Name:              "orders_tenant_customer_fkey",
				Columns:           []string{"tenant_id", "customer_id"},
				ReferencedSchema:  "crm",
				ReferencedTable:   "tenant_customers",
				ReferencedColumns: []string{"tenant_id", "customer_id"},
				OnDelete:          schema.SetNull,
			},
		},
		Relationships: []schema.RelationshipDef{
			{
				Name:              "customer",
				Kind:              schema.RelationshipBelongsTo,
				Columns:           []string{"customer_id"},
				ReferencedTable:   "customers",
				ReferencedColumns: []string{"id"},
			},
		},
	}

	require.Equal(t, literal, built)
	require.NoError(t, literal.Validate())
}
