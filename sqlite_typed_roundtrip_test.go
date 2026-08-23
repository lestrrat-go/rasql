package rasql_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteTypedSelectRoundTripsBooleanAndTime(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type event struct {
		ID        int64     `rasql:"id"`
		Active    bool      `rasql:"active"`
		CreatedAt time.Time `rasql:"created_at"`
	}
	events, err := rasql.TableOf[event](schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "active", Type: schema.BooleanType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, events))

	expected := event{
		ID:        42,
		Active:    true,
		CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
	}
	_, err = rasql.Insert(t.Context(), db, events, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(events).WhereEqual(eventID, expected.ID).One(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	_, err = rasql.SelectFrom(events).WhereEqual(eventID, expected.ID+1).One(t.Context(), db)
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestSQLiteTypedSelectWhereInFiltersRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))

	inserted := []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	}
	for _, row := range inserted {
		_, err = rasql.Insert(t.Context(), db, users, row)
		require.NoError(t, err)
	}

	actual, err := rasql.SelectFrom(users).
		WhereIn(userID, inserted[0].ID, inserted[2].ID).
		OrderAsc(userID).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []user{inserted[0], inserted[2]}, actual)
}

// TestSQLiteTypedSelectSubqueryFiltersRows runs InSelect and Scalar against a
// real SQLite database: InSelect keeps users who placed a high-value order, and
// Scalar keeps orders at or above the average order amount.
func TestSQLiteTypedSelectSubqueryFiltersRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	userEmail, err := users.Column("email")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))

	type orderRow struct {
		ID     int64 `rasql:"id"`
		UserID int64 `rasql:"user_id"`
		Amount int64 `rasql:"amount"`
	}
	orders, err := rasql.TableOf[orderRow](schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)
	amount, err := orders.Column("amount")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, orders))

	insertedUsers := []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	}
	for _, row := range insertedUsers {
		_, err = rasql.Insert(t.Context(), db, users, row)
		require.NoError(t, err)
	}
	for _, row := range []orderRow{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 2, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		_, err = rasql.Insert(t.Context(), db, orders, row)
		require.NoError(t, err)
	}

	highSpenders, err := query.NewSelect(orders.Ref(), orderUserID)
	require.NoError(t, err)
	highSpenders, err = highSpenders.WithWhere(query.GreaterThan(amount, query.Bind(50)))
	require.NoError(t, err)

	viaInSelect, err := rasql.SelectFrom(users).
		Where(query.InSelect(userID, highSpenders)).
		OrderAsc(userID).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []user{insertedUsers[0], insertedUsers[2]}, viaInSelect)

	average, err := query.NewSelect(orders.Ref(), query.Project(query.Avg(amount)))
	require.NoError(t, err)

	viaScalar, err := rasql.DecodeFrom[user](users).
		Project(userID, userEmail).
		Join(rasql.InnerJoin(orders, query.Equal(userID, orderUserID))).
		Where(query.GreaterThanOrEqual(amount, query.Scalar(average))).
		OrderAsc(userID).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []user{insertedUsers[0], insertedUsers[2]}, viaScalar)
}

// TestSQLiteTypedSelectScalarFunctionsFilterRows runs LOWER and COALESCE
// against a real SQLite database. LOWER(email) matches a mixed-case row
// against a lower-case bound value, and COALESCE(score, 0) both drops a row
// whose score is NULL from a predicate and decodes as 0 when projected,
// which is the assertion that proves the rendered text executes and its
// result decodes rather than only that a predicate filtered correctly.
func TestSQLiteTypedSelectScalarFunctionsFilterRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
		Score *int64 `rasql:"score"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "score", Type: schema.IntegerType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	score, err := users.Column("score")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))

	ten := int64(10)
	inserted := []user{
		{ID: 1, Email: "Ada@Example.com", Score: &ten},
		{ID: 2, Email: "bob@example.com", Score: nil},
	}
	for _, row := range inserted {
		_, err = rasql.Insert(t.Context(), db, users, row)
		require.NoError(t, err)
	}

	byLowerEmail, err := rasql.SelectFrom(users).
		Where(query.Equal(query.Lower(email), query.Bind("ada@example.com"))).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []user{inserted[0]}, byLowerEmail)

	// COALESCE(score, 0) > 0 drops the NULL row: NULL coalesces to 0, which
	// fails the comparison, while the row scored 10 keeps it.
	byScore, err := rasql.SelectFrom(users).
		Where(query.GreaterThan(query.Coalesce(score, query.Bind(0)), query.Bind(0))).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []user{inserted[0]}, byScore)

	type scoreRow struct {
		ID    int64 `rasql:"id"`
		Score int64 `rasql:"score"`
	}
	decoded, err := rasql.DecodeFrom[scoreRow](users).
		Project(userID, query.Project(query.Coalesce(score, query.Bind(0))).As("score")).
		OrderAsc(userID).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []scoreRow{
		{ID: 1, Score: 10},
		{ID: 2, Score: 0},
	}, decoded)
}

func TestSQLiteTypedSelectCountsRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type event struct {
		ID     int64 `rasql:"id"`
		Active bool  `rasql:"active"`
	}
	events, err := rasql.TableOf[event](schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "active", Type: schema.BooleanType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventActive, err := events.Column("active")
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, events))

	for _, row := range []event{
		{ID: 1, Active: true},
		{ID: 2, Active: true},
		{ID: 3, Active: false},
	} {
		_, err = rasql.Insert(t.Context(), db, events, row)
		require.NoError(t, err)
	}

	total, err := rasql.SelectFrom(events).Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)

	active, err := rasql.SelectFrom(events).WhereEqual(eventActive, true).Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(2), active)

	// Two predicates must both reach the counted statement, so the count has
	// to drop the inactive row and the second active row alike.
	activeFirst, err := rasql.SelectFrom(events).
		WhereEqual(eventActive, true).
		WhereEqual(eventID, int64(1)).
		Count(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(1), activeFirst)
}

// generatedEventRow has no ScanRow method, so SelectFrom falls back to
// field-mapping decode. Its untagged fields snake-case to the table's column
// names, so the fallback maps it without help.
type generatedEventRow struct {
	ID        int64
	Active    bool
	CreatedAt time.Time
	Note      *string
}

func (r generatedEventRow) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return r.ID, true
	case "active":
		return r.Active, true
	case "created_at":
		return r.CreatedAt, true
	case "note":
		return r.Note, true
	}
	return nil, false
}

func TestSQLiteGeneratedRowMethodsRoundTrip(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	events, err := rasql.TableOf[generatedEventRow](schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "active", Type: schema.BooleanType{}},
			{Name: "created_at", Type: schema.TimeType{}},
			{Name: "note", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, events))

	note := "first"
	expected := generatedEventRow{
		ID:        42,
		Active:    true,
		CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
		Note:      &note,
	}
	// Insert reaches ColumnValue, and One reaches the field-mapping decode fallback.
	_, err = rasql.Insert(t.Context(), db, events, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(events).WhereEqual(eventID, expected.ID).One(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	// A NULL column decodes back into a nil pointer.
	updated := expected
	updated.Note = nil
	_, err = rasql.Update(t.Context(), db, events, updated)
	require.NoError(t, err)

	actual, err = rasql.SelectFrom(events).WhereEqual(eventID, expected.ID).One(t.Context(), db)
	require.NoError(t, err)
	require.Nil(t, actual.Note)
}

// TestSQLiteDecimalRoundTripsExactly is the test that would have caught the
// NUMERIC(19,4)-to-REAL truncation change 2 documents: SQLite has no exact
// decimal storage class, so a DecimalType column is declared TEXT and the
// inserted digits must come back byte-identical rather than rounded through
// float64. Byte-identical is a property of SQLite's TEXT storage, not of the
// decimal type: PostgreSQL and MySQL return a decimal in its column's
// declared scale, zero-padded on the right, which TestDatabaseIntegration
// pins against the live servers.
func TestSQLiteDecimalRoundTripsExactly(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type invoice struct {
		ID     int64  `rasql:"id"`
		Amount string `rasql:"amount"`
	}
	invoices, err := rasql.TableOf[invoice](schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	invoiceID, err := invoices.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, invoices))

	expected := invoice{ID: 1, Amount: "1234.5678901234567890"}
	_, err = rasql.Insert(t.Context(), db, invoices, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(invoices).WhereEqual(invoiceID, expected.ID).One(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

// TestSQLiteQualifiedTableRoundTrip runs select, join, group, subquery,
// multi-row insert, update and delete against a schema-qualified table over
// a real SQLite database with a second database attached, pinning the
// rendered text against a real parser rather than a golden string. The
// qualified table is created through rasql.CreateTable itself, which now renders
// CREATE TABLE into the named database rather than dropping the qualifier;
// only the attached database's existence stands in for a reviewed native
// migration, the same way a native migration creates a PostgreSQL schema or
// a MySQL database in production.
func TestSQLiteQualifiedTableRoundTrip(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(t.Context(), `ATTACH DATABASE ':memory:' AS audit`)
	require.NoError(t, err)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)

	type eventRow struct {
		ID     int64  `rasql:"id"`
		UserID int64  `rasql:"user_id"`
		Action string `rasql:"action"`
	}
	events, err := rasql.TableOf[eventRow](schema.TableDef{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "action", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, events))
	queryEvents := events.Ref()
	id, err := queryEvents.Column("id")
	require.NoError(t, err)
	userID, err := queryEvents.Column("user_id")
	require.NoError(t, err)
	action, err := queryEvents.Column("action")
	require.NoError(t, err)

	// Multi-row INSERT into the qualified table.
	insertRows, err := query.NewInsertRows(queryEvents, []query.ColumnRef{id, userID, action}, [][]query.Expression{
		{query.Bind(int64(1)), query.Bind(int64(10)), query.Bind("created")},
		{query.Bind(int64(2)), query.Bind(int64(10)), query.Bind("updated")},
		{query.Bind(int64(3)), query.Bind(int64(11)), query.Bind("created")},
	})
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, insertRows)
	require.NoError(t, err)

	// SELECT with a qualified predicate.
	byUser, err := rasql.SelectFrom(events).
		Where(query.Equal(userID, query.Bind(int64(10)))).
		OrderAsc(id).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []eventRow{
		{ID: 1, UserID: 10, Action: "created"},
		{ID: 2, UserID: 10, Action: "updated"},
	}, byUser)

	// A grouped projection over the qualified table.
	type userEventCount struct {
		UserID int64 `rasql:"user_id"`
		Total  int64 `rasql:"total"`
	}
	grouped, err := rasql.DecodeFrom[userEventCount](events).
		Project(userID, query.Project(query.CountAll()).As("total")).
		GroupBy(userID).
		OrderAsc(userID).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []userEventCount{
		{UserID: 10, Total: 2},
		{UserID: 11, Total: 1},
	}, grouped)

	// A subquery naming the qualified table, both as the outer and inner
	// statement.
	prolific, err := query.NewSelect(queryEvents, userID)
	require.NoError(t, err)
	prolific, err = prolific.WithGroupBy(userID)
	require.NoError(t, err)
	prolific, err = prolific.WithHaving(query.GreaterThan(query.CountAll(), query.Bind(1)))
	require.NoError(t, err)
	viaSubquery, err := rasql.SelectFrom(events).
		Where(query.InSelect(userID, prolific)).
		OrderAsc(id).
		All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []eventRow{
		{ID: 1, UserID: 10, Action: "created"},
		{ID: 2, UserID: 10, Action: "updated"},
	}, viaSubquery)

	// UPDATE against the qualified table, with a qualified predicate.
	update, err := query.NewUpdate(queryEvents, query.Set(action, query.Bind("closed")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(int64(1))))
	require.NoError(t, err)
	_, err = rasql.Exec(t.Context(), db, update)
	require.NoError(t, err)

	updated, err := rasql.SelectFrom(events).WhereEqual(id, int64(1)).One(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, "closed", updated.Action)

	// DELETE against the qualified table, with a qualified predicate.
	_, err = rasql.DeleteFrom(events).WhereEqual(id, int64(3)).Exec(t.Context(), db)
	require.NoError(t, err)

	remaining, err := rasql.SelectFrom(events).OrderAsc(id).All(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []eventRow{
		{ID: 1, UserID: 10, Action: "closed"},
		{ID: 2, UserID: 10, Action: "updated"},
	}, remaining)
}

// TestSQLiteReturningRoundTrip exercises QueryWrite against a real
// database: an INSERT reads back a database-assigned id and a defaulted
// column through QueryWriteOne, then an UPDATE and a DELETE each read back
// their affected rows through QueryWriteAll.
func TestSQLiteReturningRoundTrip(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type returningUser struct {
		ID     int64  `rasql:"id"`
		Email  string `rasql:"email"`
		Status string `rasql:"status"`
	}
	table := schema.TableDef{
		Name: "returning_users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
		},
		PrimaryKey: []string{"id"},
	}
	users, err := rasql.TableOf[returningUser](table)
	require.NoError(t, err)
	queryUsers := users.Ref()
	id, err := queryUsers.Column("id")
	require.NoError(t, err)
	email, err := queryUsers.Column("email")
	require.NoError(t, err)
	status, err := queryUsers.Column("status")
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))

	insert, err := query.NewInsert(queryUsers, []query.ColumnRef{email}, []query.Expression{query.Bind("ada@example.com")})
	require.NoError(t, err)
	insert, err = insert.WithReturning(id, email, status)
	require.NoError(t, err)
	inserted, err := rasql.QueryWriteOne[returningUser](t.Context(), db, insert)
	require.NoError(t, err)
	require.Equal(t, returningUser{ID: 1, Email: "ada@example.com", Status: "pending"}, inserted)

	update, err := query.NewUpdate(queryUsers, query.Set(status, query.Bind("active")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(inserted.ID)))
	require.NoError(t, err)
	update, err = update.WithReturning(id, email, status)
	require.NoError(t, err)
	updated, err := rasql.QueryWriteAll[returningUser](t.Context(), db, update)
	require.NoError(t, err)
	require.Equal(t, []returningUser{{ID: 1, Email: "ada@example.com", Status: "active"}}, updated)

	deleteStatement, err := query.NewDelete(queryUsers)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(inserted.ID)))
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithReturning(id)
	require.NoError(t, err)
	type deletedRow struct {
		ID int64 `rasql:"id"`
	}
	deleted, err := rasql.QueryWriteAll[deletedRow](t.Context(), db, deleteStatement)
	require.NoError(t, err)
	require.Equal(t, []deletedRow{{ID: 1}}, deleted)

	remaining, err := rasql.SelectFrom(users).All(t.Context(), db)
	require.NoError(t, err)
	require.Empty(t, remaining)
}
