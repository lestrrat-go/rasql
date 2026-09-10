//go:build unix

package rasql_test

import (
	"database/sql"
	"math/big"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// coalescedAmounts is what the COALESCE projection in testDatabaseIntegration
// reads back for the two inserted "amount" values. The two live dialects do
// not agree, so each states its own pair rather than reusing the plain-column
// expectations the rest of the test uses; the comment on that projection says
// why MySQL differs. Both pairs are exact strings on purpose -- the point of
// the projection is to pin what a caller decoding a coalesced decimal on each
// dialect actually receives, so neither may be loosened to accept any string.
type coalescedAmounts struct {
	first  string
	second string
}

type integrationRecord struct {
	ID     int64  `rasql:"id"`
	Active bool   `rasql:"active"`
	Email  string `rasql:"email"`
	Amount string `rasql:"amount"`
}

type integrationRecordDecoder struct{ schema rasql.ResultSchema }

func (d integrationRecordDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (integrationRecordDecoder) Presence() []rasql.Presence         { return nil }
func (integrationRecordDecoder) DecodeRow(source rasql.ScanSource, row *integrationRecord) error {
	return source.Scan(&row.ID, &row.Active, &row.Email, &row.Amount)
}

type integrationAmountRow struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

func TestDatabaseIntegration(t *testing.T) {
	for _, test := range []struct {
		name      string
		open      func(*testing.T) *sql.DB
		dialect   dialect.Dialect
		coalesced coalescedAmounts
	}{
		{
			name:      "postgresql",
			open:      dbtest.PostgreSQLDB,
			dialect:   dialect.PostgreSQL(),
			coalesced: coalescedAmounts{first: "19.9900", second: "5.0000"},
		},
		{
			name:      "mysql",
			open:      dbtest.MySQLDB,
			dialect:   dialect.MySQL(),
			coalesced: coalescedAmounts{first: "19.990000000000000000000000000000", second: "5.000000000000000000000000000000"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDatabaseIntegration(t, test.open(t), test.dialect, test.coalesced)
		})
	}
}

func testDatabaseIntegration(t *testing.T, database *sql.DB, d dialect.Dialect, coalesced coalescedAmounts) {
	db, err := rasql.New(database, d)
	require.NoError(t, err)
	executor := integrationExecutor(t, db, d)
	// A fixed table name here would be inherited into every fresh PostgreSQL
	// database this test runs against: CREATE DATABASE copies template1 by
	// default, and an object added to template1 is copied into every
	// database created afterward, including the per-run database
	// dbtest.PostgreSQLDB just created. A per-run unique name keeps this
	// test from ever dropping a table it did not itself create -- the same
	// containment rule internal/dbtest's package doc states for the
	// database and role names a live test creates directly.
	tableName := dbtest.UniqueName(t, "rasql_integration_records")
	records, err := rasql.TableOf[integrationRecord](integrationTable(tableName))
	require.NoError(t, err)
	relation, err := rasql.SourceOf(records, "")
	require.NoError(t, err)
	recordID, err := rasql.BindColumn[integrationRecord, int64](relation, "id", "")
	require.NoError(t, err)
	recordActive, err := rasql.BindColumn[integrationRecord, bool](relation, "active", "")
	require.NoError(t, err)
	recordEmail, err := rasql.BindColumn[integrationRecord, string](relation, "email", "")
	require.NoError(t, err)
	recordAmount, err := rasql.BindColumn[integrationRecord, string](relation, "amount", "")
	require.NoError(t, err)
	recordSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "active", Type: schema.BooleanType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
	)
	require.NoError(t, err)
	recordProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", recordID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("active", recordActive.Expr(), schema.BooleanType{}, ""),
		rasql.Item("email", recordEmail.Expr(), schema.TextType{}, ""),
		rasql.Item("amount", recordAmount.Expr(), schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}, ""),
	}, integrationRecordDecoder{schema: recordSchema})
	require.NoError(t, err)

	_, err = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
		require.NoError(t, err)
	}()
	require.NoError(t, rasql.CreateTable(t.Context(), db, records))

	first := integrationRecord{ID: 1, Active: true, Email: "ada@example.com", Amount: "19.99"}
	second := integrationRecord{ID: 2, Active: false, Email: "grace@example.com", Amount: "5.00"}
	firstCreate, err := rasql.NewCreatePlan(records,
		rasql.SetField(recordID, first.ID), rasql.SetField(recordActive, first.Active),
		rasql.SetField(recordEmail, first.Email), rasql.SetField(recordAmount, first.Amount))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, firstCreate)
	require.NoError(t, err)
	secondCreate, err := rasql.NewCreatePlan(records,
		rasql.SetField(recordID, second.ID), rasql.SetField(recordActive, second.Active),
		rasql.SetField(recordEmail, second.Email), rasql.SetField(recordAmount, second.Amount))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, secondCreate)
	require.NoError(t, err)

	first.Email = "ada.lovelace@example.com"
	firstPatch, err := rasql.NewPatchPlan(records, rasql.EqualValue(recordID.Expr(), first.ID),
		rasql.SetField(recordActive, first.Active), rasql.SetField(recordEmail, first.Email),
		rasql.SetField(recordAmount, first.Amount))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, firstPatch)
	require.NoError(t, err)

	// PostgreSQL and MySQL both return an exact decimal in the scale its
	// column declares, zero-padded on the right. The "amount" column here is
	// declared Scale 4, so it is NUMERIC(19,4) on PostgreSQL and
	// DECIMAL(19,4) on MySQL, and the inserted "19.99" reads back as
	// "19.9900" while "5.00" reads back as "5.0000". That padding is the
	// column's declared scale, which is precisely the information an exact
	// decimal type exists to preserve, so rasql surfaces the server's digits
	// unchanged rather than trimming them. The expectations below therefore
	// state the padded form deliberately -- do not "correct" them back to the
	// shorter literals that were inserted. That declared scale governs the
	// column read directly; a projected expression over it need not keep it,
	// which is why the COALESCE projection further down carries its own
	// per-dialect expectation instead of reusing these two.
	firstStored := first
	firstStored.Amount = "19.9900"
	secondStored := second
	secondStored.Amount = "5.0000"

	selectRecords := rasql.Select(relation.Source(), recordProjection)
	actual, err := rasql.One(t.Context(), executor, selectRecords.Where(rasql.EqualValue(recordID.Expr(), first.ID)))
	require.NoError(t, err)
	require.Equal(t, firstStored, actual)

	all, err := rasql.All(t.Context(), executor, selectRecords.OrderBy(rasql.AscExpr(recordID.Expr())))
	require.NoError(t, err)
	require.Equal(t, []integrationRecord{firstStored, secondStored}, all)

	// An InSelect predicate exercises IN (SELECT …) against a real server,
	// which is what proves the MySQL rendering path this change adds actually
	// runs: MySQL is the one dialect among the two here whose grammar this
	// change had to fit without a capability gap. The row it reads back comes
	// from the server, so it carries the padded amount for the same reason the
	// two expectations above do -- expect firstStored, never first.
	recordIDRef := records.Column("id")
	recordActiveRef := records.Column("active")
	recordEmailRef := records.Column("email")
	recordAmountRef := records.Column("amount")
	activeIDs, err := query.NewSelect(records.Ref(), recordIDRef)
	require.NoError(t, err)
	activeIDs, err = activeIDs.WithWhere(query.Equal(recordActiveRef, query.Bind(true)))
	require.NoError(t, err)
	viaSubqueryQuery, err := query.NewSelect(records.Ref(), recordIDRef, recordActiveRef, recordEmailRef, recordAmountRef)
	require.NoError(t, err)
	viaSubqueryQuery, err = viaSubqueryQuery.WithWhere(query.InSelect(recordIDRef, activeIDs))
	require.NoError(t, err)
	viaSubqueryQuery, err = viaSubqueryQuery.WithOrder(query.Asc(recordIDRef))
	require.NoError(t, err)
	viaSubquery := integrationRecordRows(t, db, d, viaSubqueryQuery)
	require.Equal(t, []integrationRecord{firstStored}, viaSubquery)

	// A scalar-function predicate and projection prove COALESCE and LOWER
	// render and execute against a real server on both live dialects: the
	// design behind this change argued no new dialect.Capability is needed
	// because all three engines spell these functions identically, and this
	// is what proves that argument against MySQL and PostgreSQL rather than
	// only against the SQLite-backed tests elsewhere in this repository.
	viaLowerQuery, err := query.NewSelect(records.Ref(), recordIDRef, recordActiveRef, recordEmailRef, recordAmountRef)
	require.NoError(t, err)
	viaLowerQuery, err = viaLowerQuery.WithWhere(query.Equal(query.Lower(recordEmailRef), query.Bind(firstStored.Email)))
	require.NoError(t, err)
	viaLower := integrationRecordRows(t, db, d, viaLowerQuery)
	require.Equal(t, []integrationRecord{firstStored}, viaLower)

	// The coalesced amount is not the plain-column amount on both dialects.
	// MySQL fixes the type of COALESCE while it prepares the statement, and
	// the placeholder query.Bind produces carries no scale of its own at that
	// point, so the whole call widens to DECIMAL(65,30) and the DECIMAL(19,4)
	// column decodes with 30 digits right of the point rather than 4.
	// PostgreSQL returns the value at its own scale. That difference is a real,
	// user-visible property of the scalar functions this change adds, and it is
	// documented on query.Coalesce and in docs/core/02-sql-builder.md; pinning it
	// here per dialect is what keeps the documentation honest. Coalescing against
	// another decimal expression rather than a bound value would dodge the
	// widening, but this projection exists to exercise a bound fallback, so it
	// states both exact strings instead.
	viaCoalesceQuery, err := query.NewSelect(records.Ref(), recordIDRef,
		query.Project(query.Coalesce(recordAmountRef, query.Bind("0.0000"))).As("amount"))
	require.NoError(t, err)
	viaCoalesceQuery, err = viaCoalesceQuery.WithOrder(query.Asc(recordIDRef))
	require.NoError(t, err)
	viaCoalesce := integrationAmountRows(t, db, d, viaCoalesceQuery)
	require.Equal(t, []integrationAmountRow{
		{ID: first.ID, Amount: coalesced.first},
		{ID: second.ID, Amount: coalesced.second},
	}, viaCoalesce)
	// Whatever scale the server chose, the pinned string must still denote the
	// number the column holds. This is what makes the widened MySQL literal
	// above a statement about formatting rather than a licence to expect any
	// value, and it catches a mistyped digit in either pair.
	requireSameDecimal(t, firstStored.Amount, coalesced.first)
	requireSameDecimal(t, secondStored.Amount, coalesced.second)

	// NULLIF is the counterexample the documentation on query.Coalesce and in
	// docs/core/02-sql-builder.md now names: the widening above is not a property of
	// mixing any function with a placeholder, it is a property of a function
	// whose result type is resolved across all of its arguments. MySQL types
	// NULLIF from its first argument alone, so a decimal column passed first
	// keeps its declared DECIMAL(19,4) and decodes at scale 4 even though the
	// second argument is a scaleless placeholder. PostgreSQL types NULLIF the
	// same way, so both live dialects read back the plain-column strings the
	// expectations above already state, and this projection needs no
	// per-dialect pair of its own. Neither amount equals the bound "0.0000",
	// so NULLIF returns the column value on every row.
	viaNullIfQuery, err := query.NewSelect(records.Ref(), recordIDRef,
		query.Project(query.Func("NULLIF", recordAmountRef, query.Bind("0.0000"))).As("amount"))
	require.NoError(t, err)
	viaNullIfQuery, err = viaNullIfQuery.WithOrder(query.Asc(recordIDRef))
	require.NoError(t, err)
	viaNullIf := integrationAmountRows(t, db, d, viaNullIfQuery)
	require.Equal(t, []integrationAmountRow{
		{ID: first.ID, Amount: firstStored.Amount},
		{ID: second.ID, Amount: secondStored.Amount},
	}, viaNullIf)

	total, err := rasql.One(t.Context(), executor, rasql.CountQuery(selectRecords, false))
	require.NoError(t, err)
	require.Equal(t, int64(2), total)

	// RETURNING is PostgreSQL-only among the two live dialects this test runs
	// against, so QueryWrite is exercised over the real pgx driver on
	// PostgreSQL and pinned as a build-time rejection on MySQL.
	third := integrationRecord{ID: 3, Active: true, Email: "grace@example.com", Amount: "42.50"}
	// RETURNING reads the row back from the server, so the decimal arrives in
	// the column's declared scale for the same reason the two expectations
	// above do.
	thirdStored := third
	thirdStored.Amount = "42.5000"
	insert, err := query.NewInsert(
		records.Ref(),
		query.Set(recordIDRef, third.ID),
		query.Set(recordActiveRef, third.Active),
		query.Set(recordEmailRef, third.Email),
		query.Set(recordAmountRef, third.Amount),
	)
	require.NoError(t, err)
	if d.Supports(dialect.CapabilityReturning) {
		insertPlan, planErr := rasql.NewStatementPlan(insert)
		require.NoError(t, planErr)
		returned, returningErr := rasql.Returning(insertPlan, recordProjection)
		require.NoError(t, returningErr)
		inserted, queryErr := rasql.One(t.Context(), executor, returned)
		require.NoError(t, queryErr)
		require.Equal(t, thirdStored, inserted)
	} else {
		returningInsert, returningErr := insert.WithReturning(recordIDRef, recordActiveRef, recordEmailRef, recordAmountRef)
		require.NoError(t, returningErr)
		_, err := exec.RenderWrite(db, returningInsert)
		require.ErrorContains(t, err, "RETURNING is not supported")
	}

	inspector, err := inspect.New(database, d)
	require.NoError(t, err)
	inspected, err := inspector.Table(t.Context(), tableName)
	require.NoError(t, err)
	require.Equal(t, integrationTable(tableName), inspected)
}

func integrationExecutor(t *testing.T, db rasql.DB, d dialect.Dialect) rasql.Executor {
	t.Helper()
	profileID := ""
	switch d.Name() {
	case "postgresql":
		profileID = "postgresql-17"
	case "mysql":
		profileID = "mysql-8.4"
	default:
		t.Fatalf("unsupported integration dialect %q", d.Name())
	}
	profile, err := rasql.DiscoverEngineProfile(t.Context(), db, profileID)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	return executor
}

func integrationRecordRows(t *testing.T, db rasql.DB, d dialect.Dialect, statement query.Select) []integrationRecord {
	t.Helper()
	rendered, err := render.Select(d, statement)
	require.NoError(t, err)
	rows, err := db.QueryRendered(t.Context(), rendered)
	require.NoError(t, err)
	var result []integrationRecord
	for rows.Next() {
		var row integrationRecord
		require.NoError(t, rows.Scan(&row.ID, &row.Active, &row.Email, &row.Amount))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return result
}

func integrationAmountRows(t *testing.T, db rasql.DB, d dialect.Dialect, statement query.Select) []integrationAmountRow {
	t.Helper()
	rendered, err := render.Select(d, statement)
	require.NoError(t, err)
	rows, err := db.QueryRendered(t.Context(), rendered)
	require.NoError(t, err)
	var result []integrationAmountRow
	for rows.Next() {
		var row integrationAmountRow
		require.NoError(t, rows.Scan(&row.ID, &row.Amount))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return result
}

// requireSameDecimal fails unless two decimal strings denote the same number.
// It parses with math/big rather than float64 so that comparing a 30-digit
// scale against a 4-digit one stays exact, which is the whole point of the
// decimal type under test.
func requireSameDecimal(t *testing.T, expected, actual string) {
	t.Helper()
	expectedValue, ok := new(big.Rat).SetString(expected)
	require.True(t, ok, "%q is not a decimal", expected)
	actualValue, ok := new(big.Rat).SetString(actual)
	require.True(t, ok, "%q is not a decimal", actual)
	require.Zero(t, expectedValue.Cmp(actualValue), "%q and %q are different numbers", expected, actual)
}

func integrationTable(name string) schema.TableDef {
	return schema.TableDef{
		Name: name,
		Kind: schema.ObjectTable,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "active", Type: schema.BooleanType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
	}
}

// TestQualifiedDDLIntegration proves the schema-qualified DDL path against
// both live servers: PostgreSQL and MySQL differ in what a "schema" is, so
// each subtest creates its own second namespace the way an application would
// -- a native CREATE SCHEMA on PostgreSQL, a second CREATE DATABASE on MySQL
// -- rather than relying on anything rasql itself creates, since creating a
// namespace stays out of scope for rasql.CreateTable. SQLite has no server and no
// DDL statement for a namespace at all (its namespace comes from ATTACH), so
// its coverage lives in render/schema_test.go's TestSQLiteExecutesQualifiedDDL
// and sqlite_typed_test.go's TestSQLiteRoundTrip/"a qualified table"
// instead of here.
func TestQualifiedDDLIntegration(t *testing.T) {
	t.Run("postgresql", testQualifiedDDLPostgreSQL)
	t.Run("mysql", testQualifiedDDLMySQL)
}

// testQualifiedDDLPostgreSQL creates a table in a fresh PostgreSQL schema
// through rasql.CreateTable, with a foreign key that reaches back into the
// connection's default "public" schema via ForeignKey.ReferencedSchema. The
// test never touches search_path: the whole point is that fully qualified
// DDL does not depend on it, and the cross-schema foreign key is what proves
// REFERENCES "public"."..." itself renders and executes.
func testQualifiedDDLPostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	executor := integrationExecutor(t, db, dialect.PostgreSQL())

	type customerRow struct {
		ID   int64  `rasql:"id"`
		Name string `rasql:"name"`
	}
	customersName := dbtest.UniqueName(t, "rasql_qualified_customers")
	customers, err := rasql.TableOf[customerRow](schema.TableDef{
		Name: customersName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	customersSource, err := rasql.SourceOf(customers, "")
	require.NoError(t, err)
	customersID, err := rasql.BindColumn[customerRow, int64](customersSource, "id", "")
	require.NoError(t, err)
	customersNameColumn, err := rasql.BindColumn[customerRow, string](customersSource, "name", "")
	require.NoError(t, err)
	// customersName lives in the connection's default schema, "public",
	// which rasql.CreateTable never states explicitly: an unqualified Schema
	// resolves through the connection's own default, the same as before
	// this change.
	require.NoError(t, rasql.CreateTable(t.Context(), db, customers))
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+customersName)
		require.NoError(t, err)
	}()

	schemaName := dbtest.UniqueName(t, "rasql_qualified_schema")
	_, err = database.ExecContext(t.Context(), "CREATE SCHEMA "+schemaName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		require.NoError(t, err)
	}()

	type orderRow struct {
		ID         int64 `rasql:"id"`
		CustomerID int64 `rasql:"customer_id"`
	}
	ordersName := dbtest.UniqueName(t, "rasql_qualified_orders")
	orders, err := rasql.TableOf[orderRow](schema.TableDef{
		Schema: schemaName,
		Name:   ordersName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              ordersName + "_customer_fkey",
			Columns:           []string{"customer_id"},
			ReferencedSchema:  "public",
			ReferencedTable:   customersName,
			ReferencedColumns: []string{"id"},
		}},
		Indexes: []schema.IndexDef{{
			Name:    ordersName + "_customer_idx",
			Columns: []string{"customer_id"},
		}},
	})
	require.NoError(t, err)
	ordersSource, err := rasql.SourceOf(orders, "")
	require.NoError(t, err)
	ordersID, err := rasql.BindColumn[orderRow, int64](ordersSource, "id", "")
	require.NoError(t, err)
	ordersCustomerID, err := rasql.BindColumn[orderRow, int64](ordersSource, "customer_id", "")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, orders))

	customerCreate, err := rasql.NewCreatePlan(customers,
		rasql.SetField(customersID, int64(1)), rasql.SetField(customersNameColumn, "ada"))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, customerCreate)
	require.NoError(t, err)
	orderCreate, err := rasql.NewCreatePlan(orders,
		rasql.SetField(ordersID, int64(1)), rasql.SetField(ordersCustomerID, int64(1)))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, orderCreate)
	require.NoError(t, err)

	orderSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "customer_id", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	orderDecoder, err := rasql.DynamicProjection[orderRow](orderSchema)
	require.NoError(t, err)
	orderProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", ordersID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("customer_id", ordersCustomerID.Expr(), schema.IntegerType{}, ""),
	}, orderDecoder.Decoder())
	require.NoError(t, err)
	orderQuery := rasql.Select(ordersSource.Source(), orderProjection).
		Where(rasql.EqualValue(ordersID.Expr(), int64(1)))
	order, err := rasql.One(t.Context(), executor, orderQuery)
	require.NoError(t, err)
	require.Equal(t, orderRow{ID: 1, CustomerID: 1}, order)
}

// testQualifiedDDLMySQL creates a table in a second MySQL database through
// rasql.CreateTable. A "schema" is a second database on MySQL, so this test
// creates one directly: dbtest.MySQLDB already grants the CREATE/DROP
// privilege CONTRIBUTING.md requires of a live MySQL test DSN, and a
// per-run-unique name keeps this inside the containment rule that a live
// test only touches objects it created.
func testQualifiedDDLMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := rasql.New(database, dialect.MySQL())
	require.NoError(t, err)
	executor := integrationExecutor(t, db, dialect.MySQL())

	schemaName := dbtest.UniqueName(t, "rasql_qualified_schema")
	_, err = database.ExecContext(t.Context(), "CREATE DATABASE "+schemaName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP DATABASE IF EXISTS "+schemaName)
		require.NoError(t, err)
	}()

	type eventRow struct {
		ID      int64  `rasql:"id"`
		ActorID int64  `rasql:"actor_id"`
		Action  string `rasql:"action"`
	}
	eventsName := dbtest.UniqueName(t, "rasql_qualified_events")
	events, err := rasql.TableOf[eventRow](schema.TableDef{
		Schema: schemaName,
		Name:   eventsName,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "actor_id", Type: schema.IntegerType{}},
			{Name: "action", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		// The index names actor_id, not the action column beside it,
		// because MySQL maps schema.TextType to TEXT and refuses an index
		// on a BLOB/TEXT column unless the index states a key length --
		// which schema.IndexDef has no field for. actor_id is a fixed-width
		// BIGINT, so it indexes on every dialect and the qualified
		// CREATE INDEX this test exists to exercise is the only thing
		// under test here.
		Indexes: []schema.IndexDef{{
			Name:    eventsName + "_actor_idx",
			Columns: []string{"actor_id"},
		}},
	})
	require.NoError(t, err)
	eventsSource, err := rasql.SourceOf(events, "")
	require.NoError(t, err)
	eventID, err := rasql.BindColumn[eventRow, int64](eventsSource, "id", "")
	require.NoError(t, err)
	eventActorID, err := rasql.BindColumn[eventRow, int64](eventsSource, "actor_id", "")
	require.NoError(t, err)
	eventAction, err := rasql.BindColumn[eventRow, string](eventsSource, "action", "")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, events))

	// Both objects must live in schemaName rather than in the connection's
	// own default database, which is what the qualified DDL is for. The
	// index is named explicitly because its CREATE INDEX qualifies the
	// table it targets rather than the index name, so nothing else here
	// would notice it landing next to the wrong table.
	var indexSchema, indexTable string
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT TABLE_SCHEMA, TABLE_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND INDEX_NAME = ?",
		schemaName, eventsName+"_actor_idx",
	).Scan(&indexSchema, &indexTable))
	require.Equal(t, schemaName, indexSchema)
	require.Equal(t, eventsName, indexTable)

	eventCreate, err := rasql.NewCreatePlan(events,
		rasql.SetField(eventID, int64(1)), rasql.SetField(eventActorID, int64(7)),
		rasql.SetField(eventAction, "created"))
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, eventCreate)
	require.NoError(t, err)

	eventSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "actor_id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "action", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	eventDecoder, err := rasql.DynamicProjection[eventRow](eventSchema)
	require.NoError(t, err)
	eventProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", eventID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("actor_id", eventActorID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("action", eventAction.Expr(), schema.TextType{}, ""),
	}, eventDecoder.Decoder())
	require.NoError(t, err)
	eventQuery := rasql.Select(eventsSource.Source(), eventProjection).
		Where(rasql.EqualValue(eventID.Expr(), int64(1)))
	event, err := rasql.One(t.Context(), executor, eventQuery)
	require.NoError(t, err)
	require.Equal(t, eventRow{ID: 1, ActorID: 7, Action: "created"}, event)
}

// TestIntegrationTableUsesItsNameArgument pins that integrationTable is
// parameterized by name rather than carrying a fixed literal: this needs no
// live server, since it is the same schema.TableDef construction
// testDatabaseIntegration feeds into rasql.TableOf, the DROP/CREATE
// statements, and inspector.Table -- all from the single tableName variable
// dbtest.UniqueName produces (see testDatabaseIntegration above). Reverting
// integrationTable to hardcode "rasql_integration_records" -- the bug this
// test exists to catch -- reintroduces the containment violation the
// package doc warns about: a table of that fixed name in PostgreSQL's
// template1 would be inherited into every fresh per-run database and then
// dropped by this test, though not this call, since two arbitrary names
// would then collide.
func TestIntegrationTableUsesItsNameArgument(t *testing.T) {
	first := integrationTable("rasql_integration_records_1")
	second := integrationTable("rasql_integration_records_2")

	if first.Name != "rasql_integration_records_1" {
		t.Fatalf("integrationTable(%q).Name = %q, want %q", "rasql_integration_records_1", first.Name, "rasql_integration_records_1")
	}
	if second.Name != "rasql_integration_records_2" {
		t.Fatalf("integrationTable(%q).Name = %q, want %q", "rasql_integration_records_2", second.Name, "rasql_integration_records_2")
	}
	if first.Name == second.Name {
		t.Fatalf("two different name arguments both produced schema.TableDef.Name %q; integrationTable must not carry a fixed table name", first.Name)
	}

	// Everything but the name must stay identical, so parameterizing the
	// name cannot silently mask an unrelated schema difference.
	first.Name, second.Name = "", ""
	require.Equal(t, first, second)
}
