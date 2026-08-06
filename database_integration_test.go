//go:build unix

package rasql_test

import (
	"database/sql"
	"math/big"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
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
	client, err := rasql.New(database, d)
	require.NoError(t, err)
	type record struct {
		ID     int64  `rasql:"id"`
		Active bool   `rasql:"active"`
		Email  string `rasql:"email"`
		Amount string `rasql:"amount"`
	}
	// A fixed table name here would be inherited into every fresh PostgreSQL
	// database this test runs against: CREATE DATABASE copies template1 by
	// default, and an object added to template1 is copied into every
	// database created afterward, including the per-run database
	// dbtest.PostgreSQLDB just created. A per-run unique name keeps this
	// test from ever dropping a table it did not itself create -- the same
	// containment rule internal/dbtest's package doc states for the
	// database and role names a live test creates directly.
	tableName := dbtest.UniqueName(t, "rasql_integration_records")
	records, err := rasql.NewTable[record](integrationTable(tableName))
	require.NoError(t, err)
	recordID, err := records.Column("id")
	require.NoError(t, err)

	_, err = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS "+tableName)
		require.NoError(t, err)
	}()
	require.NoError(t, rasql.Create(t.Context(), client, records))

	first := record{ID: 1, Active: true, Email: "ada@example.com", Amount: "19.99"}
	second := record{ID: 2, Active: false, Email: "grace@example.com", Amount: "5.00"}
	_, err = rasql.Insert(t.Context(), client, records, first)
	require.NoError(t, err)
	_, err = rasql.Insert(t.Context(), client, records, second)
	require.NoError(t, err)

	first.Email = "ada.lovelace@example.com"
	_, err = rasql.Update(t.Context(), client, records, first)
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

	actual, err := rasql.SelectFrom(records).WhereEqual(recordID, first.ID).One(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, firstStored, actual)

	all, err := rasql.SelectFrom(records).OrderAsc(recordID).All(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, []record{firstStored, secondStored}, all)

	// An InSelect predicate exercises IN (SELECT …) against a real server,
	// which is what proves the MySQL rendering path this change adds actually
	// runs: MySQL is the one dialect among the two here whose grammar this
	// change had to fit without a capability gap. The row it reads back comes
	// from the server, so it carries the padded amount for the same reason the
	// two expectations above do -- expect firstStored, never first.
	recordActive, err := records.Column("active")
	require.NoError(t, err)
	activeIDs, err := query.NewSelect(records.QueryTable(), query.Project(recordID))
	require.NoError(t, err)
	activeIDs, err = activeIDs.WithWhere(query.Equal(recordActive, query.Bind(true)))
	require.NoError(t, err)
	viaSubquery, err := rasql.SelectFrom(records).
		Where(query.InSelect(recordID, activeIDs)).
		OrderAsc(recordID).
		All(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, []record{firstStored}, viaSubquery)

	// A scalar-function predicate and projection prove COALESCE and LOWER
	// render and execute against a real server on both live dialects: the
	// design behind this change argued no new dialect.Capability is needed
	// because all three engines spell these functions identically, and this
	// is what proves that argument against MySQL and PostgreSQL rather than
	// only against the SQLite-backed tests elsewhere in this repository.
	scalarEmail, err := records.Column("email")
	require.NoError(t, err)
	scalarAmount, err := records.Column("amount")
	require.NoError(t, err)
	viaLower, err := rasql.SelectFrom(records).
		Where(query.Equal(query.Lower(scalarEmail), query.Bind(firstStored.Email))).
		All(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, []record{firstStored}, viaLower)

	type amountRow struct {
		ID     int64  `rasql:"id"`
		Amount string `rasql:"amount"`
	}
	// The coalesced amount is not the plain-column amount on both dialects.
	// MySQL fixes the type of COALESCE while it prepares the statement, and
	// the placeholder query.Bind produces carries no scale of its own at that
	// point, so the whole call widens to DECIMAL(65,30) and the DECIMAL(19,4)
	// column decodes with 30 digits right of the point rather than 4.
	// PostgreSQL returns the value at its own scale. That difference is a real,
	// user-visible property of the scalar functions this change adds, and it is
	// documented on query.Coalesce and in docs/03-querying.md; pinning it here
	// per dialect is what keeps the documentation honest. Coalescing against
	// another decimal expression rather than a bound value would dodge the
	// widening, but this projection exists to exercise a bound fallback, so it
	// states both exact strings instead.
	viaCoalesce, err := rasql.DecodeFrom[amountRow](records).
		Project(query.Project(recordID), query.Project(query.Coalesce(scalarAmount, query.Bind("0.0000"))).As("amount")).
		OrderAsc(recordID).
		All(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, []amountRow{
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
	// docs/03-querying.md now names: the widening above is not a property of
	// mixing any function with a placeholder, it is a property of a function
	// whose result type is resolved across all of its arguments. MySQL types
	// NULLIF from its first argument alone, so a decimal column passed first
	// keeps its declared DECIMAL(19,4) and decodes at scale 4 even though the
	// second argument is a scaleless placeholder. PostgreSQL types NULLIF the
	// same way, so both live dialects read back the plain-column strings the
	// expectations above already state, and this projection needs no
	// per-dialect pair of its own. Neither amount equals the bound "0.0000",
	// so NULLIF returns the column value on every row.
	viaNullIf, err := rasql.DecodeFrom[amountRow](records).
		Project(query.Project(recordID), query.Project(query.Func("NULLIF", scalarAmount, query.Bind("0.0000"))).As("amount")).
		OrderAsc(recordID).
		All(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, []amountRow{
		{ID: first.ID, Amount: firstStored.Amount},
		{ID: second.ID, Amount: secondStored.Amount},
	}, viaNullIf)

	total, err := rasql.SelectFrom(records).Count(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)

	// RETURNING is PostgreSQL-only among the two live dialects this test runs
	// against, so QueryWrite is exercised over the real pgx driver on
	// PostgreSQL and pinned as a build-time rejection on MySQL.
	recordEmail, err := records.Column("email")
	require.NoError(t, err)
	recordAmount, err := records.Column("amount")
	require.NoError(t, err)
	third := record{ID: 3, Active: true, Email: "grace@example.com", Amount: "42.50"}
	// RETURNING reads the row back from the server, so the decimal arrives in
	// the column's declared scale for the same reason the two expectations
	// above do.
	thirdStored := third
	thirdStored.Amount = "42.5000"
	insert, err := query.NewInsert(
		records.QueryTable(),
		[]query.Column{recordID, recordActive, recordEmail, recordAmount},
		[]query.Expression{query.Bind(third.ID), query.Bind(third.Active), query.Bind(third.Email), query.Bind(third.Amount)},
	)
	require.NoError(t, err)
	insert, err = insert.WithReturning(query.Project(recordID), query.Project(recordActive), query.Project(recordEmail), query.Project(recordAmount))
	require.NoError(t, err)
	if d.Supports(dialect.CapabilityReturning) {
		inserted, err := rasql.QueryWriteOne[record](t.Context(), client, insert)
		require.NoError(t, err)
		require.Equal(t, thirdStored, inserted)
	} else {
		_, err := rasql.QueryWrite(t.Context(), client, insert)
		require.ErrorContains(t, err, "RETURNING is not supported")
	}

	inspector, err := inspect.New(database, d)
	require.NoError(t, err)
	inspected, err := inspector.Table(t.Context(), tableName)
	require.NoError(t, err)
	require.Equal(t, integrationTable(tableName), inspected)
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

func integrationTable(name string) schema.Table {
	return schema.Table{
		Name: name,
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
			{Name: "email", Type: schema.TypeText},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4)},
		},
		PrimaryKey: []string{"id"},
	}
}

// TestQualifiedDDLIntegration proves the schema-qualified DDL path against
// both live servers: PostgreSQL and MySQL differ in what a "schema" is, so
// each subtest creates its own second namespace the way an application would
// -- a native CREATE SCHEMA on PostgreSQL, a second CREATE DATABASE on MySQL
// -- rather than relying on anything rasql itself creates, since creating a
// namespace stays out of scope for rasql.Create. SQLite has no server and no
// DDL statement for a namespace at all (its namespace comes from ATTACH), so
// its coverage lives in render/schema_test.go's TestSQLiteExecutesQualifiedDDL
// and sqlite_typed_roundtrip_test.go's TestSQLiteQualifiedTableRoundTrip
// instead of here.
func TestQualifiedDDLIntegration(t *testing.T) {
	t.Run("postgresql", testQualifiedDDLPostgreSQL)
	t.Run("mysql", testQualifiedDDLMySQL)
}

// testQualifiedDDLPostgreSQL creates a table in a fresh PostgreSQL schema
// through rasql.Create, with a foreign key that reaches back into the
// connection's default "public" schema via ForeignKey.ReferencedSchema. The
// test never touches search_path: the whole point is that fully qualified
// DDL does not depend on it, and the cross-schema foreign key is what proves
// REFERENCES "public"."..." itself renders and executes.
func testQualifiedDDLPostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	client, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)

	type customerRow struct {
		ID   int64  `rasql:"id"`
		Name string `rasql:"name"`
	}
	customersName := dbtest.UniqueName(t, "rasql_qualified_customers")
	customers, err := rasql.NewTable[customerRow](schema.Table{
		Name: customersName,
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "name", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	// customersName lives in the connection's default schema, "public",
	// which rasql.Create never states explicitly: an unqualified Schema
	// resolves through the connection's own default, the same as before
	// this change.
	require.NoError(t, rasql.Create(t.Context(), client, customers))
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
	orders, err := rasql.NewTable[orderRow](schema.Table{
		Schema: schemaName,
		Name:   ordersName,
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "customer_id", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []schema.ForeignKey{{
			Name:              ordersName + "_customer_fkey",
			Columns:           []string{"customer_id"},
			ReferencedSchema:  "public",
			ReferencedTable:   customersName,
			ReferencedColumns: []string{"id"},
		}},
		Indexes: []schema.Index{{
			Name:    ordersName + "_customer_idx",
			Columns: []string{"customer_id"},
		}},
	})
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, orders))

	_, err = rasql.Insert(t.Context(), client, customers, customerRow{ID: 1, Name: "ada"})
	require.NoError(t, err)
	_, err = rasql.Insert(t.Context(), client, orders, orderRow{ID: 1, CustomerID: 1})
	require.NoError(t, err)

	ordersID, err := orders.Column("id")
	require.NoError(t, err)
	order, err := rasql.SelectFrom(orders).WhereEqual(ordersID, int64(1)).One(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, orderRow{ID: 1, CustomerID: 1}, order)
}

// testQualifiedDDLMySQL creates a table in a second MySQL database through
// rasql.Create. A "schema" is a second database on MySQL, so this test
// creates one directly: dbtest.MySQLDB already grants the CREATE/DROP
// privilege CONTRIBUTING.md requires of a live MySQL test DSN, and a
// per-run-unique name keeps this inside the containment rule that a live
// test only touches objects it created.
func testQualifiedDDLMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	client, err := rasql.New(database, dialect.MySQL())
	require.NoError(t, err)

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
	events, err := rasql.NewTable[eventRow](schema.Table{
		Schema: schemaName,
		Name:   eventsName,
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "actor_id", Type: schema.TypeInteger},
			{Name: "action", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
		// The index names actor_id, not the action column beside it,
		// because MySQL maps schema.TypeText to TEXT and refuses an index
		// on a BLOB/TEXT column unless the index states a key length --
		// which schema.Index has no field for. actor_id is a fixed-width
		// BIGINT, so it indexes on every dialect and the qualified
		// CREATE INDEX this test exists to exercise is the only thing
		// under test here.
		Indexes: []schema.Index{{
			Name:    eventsName + "_actor_idx",
			Columns: []string{"actor_id"},
		}},
	})
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

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

	_, err = rasql.Insert(t.Context(), client, events, eventRow{ID: 1, ActorID: 7, Action: "created"})
	require.NoError(t, err)

	eventID, err := events.Column("id")
	require.NoError(t, err)
	event, err := rasql.SelectFrom(events).WhereEqual(eventID, int64(1)).One(t.Context(), client)
	require.NoError(t, err)
	require.Equal(t, eventRow{ID: 1, ActorID: 7, Action: "created"}, event)
}

// TestIntegrationTableUsesItsNameArgument pins that integrationTable is
// parameterized by name rather than carrying a fixed literal: this needs no
// live server, since it is the same schema.Table construction
// testDatabaseIntegration feeds into rasql.NewTable, the DROP/CREATE
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
		t.Fatalf("two different name arguments both produced schema.Table.Name %q; integrationTable must not carry a fixed table name", first.Name)
	}

	// Everything but the name must stay identical, so parameterizing the
	// name cannot silently mask an unrelated schema difference.
	first.Name, second.Name = "", ""
	require.Equal(t, first, second)
}
