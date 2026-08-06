//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestDatabaseIntegration(t *testing.T) {
	for _, test := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect dialect.Dialect
	}{
		{
			name:    "postgresql",
			open:    dbtest.PostgreSQLDB,
			dialect: dialect.PostgreSQL(),
		},
		{
			name:    "mysql",
			open:    dbtest.MySQLDB,
			dialect: dialect.MySQL(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDatabaseIntegration(t, test.open(t), test.dialect)
		})
	}
}

func testDatabaseIntegration(t *testing.T, database *sql.DB, d dialect.Dialect) {
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
	// shorter literals that were inserted.
	firstStored := first
	firstStored.Amount = "19.9900"
	secondStored := second
	secondStored.Amount = "5.0000"

	actual, err := rasql.SelectFrom(client, records).WhereEqual(recordID, first.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, firstStored, actual)

	all, err := rasql.SelectFrom(client, records).OrderAsc(recordID).All(t.Context())
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
	viaSubquery, err := rasql.SelectFrom(client, records).
		Where(query.InSelect(recordID, activeIDs)).
		OrderAsc(recordID).
		All(t.Context())
	require.NoError(t, err)
	require.Equal(t, []record{firstStored}, viaSubquery)

	total, err := rasql.SelectFrom(client, records).Count(t.Context())
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
		_, err := client.QueryWrite(t.Context(), insert)
		require.ErrorContains(t, err, "RETURNING is not supported")
	}

	inspector, err := inspect.New(database, d)
	require.NoError(t, err)
	inspected, err := inspector.Table(t.Context(), tableName)
	require.NoError(t, err)
	require.Equal(t, integrationTable(tableName), inspected)
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
	order, err := rasql.SelectFrom(client, orders).WhereEqual(ordersID, int64(1)).One(t.Context())
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
		ID     int64  `rasql:"id"`
		Action string `rasql:"action"`
	}
	eventsName := dbtest.UniqueName(t, "rasql_qualified_events")
	events, err := rasql.NewTable[eventRow](schema.Table{
		Schema: schemaName,
		Name:   eventsName,
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "action", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.Index{{
			Name:    eventsName + "_action_idx",
			Columns: []string{"action"},
		}},
	})
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

	_, err = rasql.Insert(t.Context(), client, events, eventRow{ID: 1, Action: "created"})
	require.NoError(t, err)

	eventID, err := events.Column("id")
	require.NoError(t, err)
	event, err := rasql.SelectFrom(client, events).WhereEqual(eventID, int64(1)).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, eventRow{ID: 1, Action: "created"}, event)
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
