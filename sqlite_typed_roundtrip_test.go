package rasql_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
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

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type event struct {
		ID        int64     `rasql:"id"`
		Active    bool      `rasql:"active"`
		CreatedAt time.Time `rasql:"created_at"`
	}
	events, err := rasql.NewTable[event](schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
			{Name: "created_at", Type: schema.TypeTime},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

	expected := event{
		ID:        42,
		Active:    true,
		CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
	}
	_, err = rasql.Insert(t.Context(), client, events, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	_, err = rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID+1).One(t.Context())
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

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, users))

	inserted := []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	}
	for _, row := range inserted {
		_, err = rasql.Insert(t.Context(), client, users, row)
		require.NoError(t, err)
	}

	actual, err := rasql.SelectFrom(client, users).
		WhereIn(userID, inserted[0].ID, inserted[2].ID).
		OrderAsc(userID).
		All(t.Context())
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

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type user struct {
		ID    int64  `rasql:"id"`
		Email string `rasql:"email"`
	}
	users, err := rasql.NewTable[user](schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	userID, err := users.Column("id")
	require.NoError(t, err)
	userEmail, err := users.Column("email")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, users))

	type orderRow struct {
		ID     int64 `rasql:"id"`
		UserID int64 `rasql:"user_id"`
		Amount int64 `rasql:"amount"`
	}
	orders, err := rasql.NewTable[orderRow](schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "user_id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orderUserID, err := orders.Column("user_id")
	require.NoError(t, err)
	amount, err := orders.Column("amount")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, orders))

	insertedUsers := []user{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	}
	for _, row := range insertedUsers {
		_, err = rasql.Insert(t.Context(), client, users, row)
		require.NoError(t, err)
	}
	for _, row := range []orderRow{
		{ID: 1, UserID: 1, Amount: 80},
		{ID: 2, UserID: 2, Amount: 20},
		{ID: 3, UserID: 3, Amount: 100},
	} {
		_, err = rasql.Insert(t.Context(), client, orders, row)
		require.NoError(t, err)
	}

	highSpenders, err := query.NewSelect(orders.QueryTable(), query.Project(orderUserID))
	require.NoError(t, err)
	highSpenders, err = highSpenders.WithWhere(query.GreaterThan(amount, query.Bind(50)))
	require.NoError(t, err)

	viaInSelect, err := rasql.SelectFrom(client, users).
		Where(query.InSelect(userID, highSpenders)).
		OrderAsc(userID).
		All(t.Context())
	require.NoError(t, err)
	require.Equal(t, []user{insertedUsers[0], insertedUsers[2]}, viaInSelect)

	average, err := query.NewSelect(orders.QueryTable(), query.Project(query.Avg(amount)))
	require.NoError(t, err)

	viaScalar, err := rasql.DecodeFrom[user](client, users).
		Project(query.Project(userID), query.Project(userEmail)).
		Join(rasql.InnerJoin(orders, query.Equal(userID, orderUserID))).
		Where(query.GreaterThanOrEqual(amount, query.Scalar(average))).
		OrderAsc(userID).
		All(t.Context())
	require.NoError(t, err)
	require.Equal(t, []user{insertedUsers[0], insertedUsers[2]}, viaScalar)
}

func TestSQLiteTypedSelectCountsRows(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type event struct {
		ID     int64 `rasql:"id"`
		Active bool  `rasql:"active"`
	}
	events, err := rasql.NewTable[event](schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventActive, err := events.Column("active")
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

	for _, row := range []event{
		{ID: 1, Active: true},
		{ID: 2, Active: true},
		{ID: 3, Active: false},
	} {
		_, err = rasql.Insert(t.Context(), client, events, row)
		require.NoError(t, err)
	}

	total, err := rasql.SelectFrom(client, events).Count(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(3), total)

	active, err := rasql.SelectFrom(client, events).WhereEqual(eventActive, true).Count(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), active)

	// Two predicates must both reach the counted statement, so the count has
	// to drop the inactive row and the second active row alike.
	activeFirst, err := rasql.SelectFrom(client, events).
		WhereEqual(eventActive, true).
		WhereEqual(eventID, int64(1)).
		Count(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), activeFirst)
}

// generatedEventRow has the shape rasqlgen emits: no tags, and one method per
// direction stating the column-to-field mapping.
type generatedEventRow struct {
	ID        int64
	Active    bool
	CreatedAt time.Time
	Note      *string
}

func (r *generatedEventRow) DecodeRow(src row.Row) error {
	if err := row.Assign(src, "id", &r.ID); err != nil {
		return err
	}
	if err := row.Assign(src, "active", &r.Active); err != nil {
		return err
	}
	if err := row.Assign(src, "created_at", &r.CreatedAt); err != nil {
		return err
	}
	return row.Assign(src, "note", &r.Note)
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

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	events, err := rasql.NewTable[generatedEventRow](schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
			{Name: "created_at", Type: schema.TypeTime},
			{Name: "note", Type: schema.TypeText, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	eventID, err := events.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, events))

	note := "first"
	expected := generatedEventRow{
		ID:        42,
		Active:    true,
		CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
		Note:      &note,
	}
	// Insert reaches ColumnValue, and One reaches DecodeRow.
	_, err = rasql.Insert(t.Context(), client, events, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	// A NULL column decodes back into a nil pointer.
	updated := expected
	updated.Note = nil
	_, err = rasql.Update(t.Context(), client, events, updated)
	require.NoError(t, err)

	actual, err = rasql.SelectFrom(client, events).WhereEqual(eventID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Nil(t, actual.Note)
}

// TestSQLiteDecimalRoundTripsExactly is the test that would have caught the
// NUMERIC(19,4)-to-REAL truncation change 2 documents: SQLite has no exact
// decimal storage class, so a TypeDecimal column is declared TEXT and the
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

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type invoice struct {
		ID     int64  `rasql:"id"`
		Amount string `rasql:"amount"`
	}
	invoices, err := rasql.NewTable[invoice](schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: 4},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	invoiceID, err := invoices.Column("id")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, invoices))

	expected := invoice{ID: 1, Amount: "1234.5678901234567890"}
	_, err = rasql.Insert(t.Context(), client, invoices, expected)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(client, invoices).WhereEqual(invoiceID, expected.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

// TestSQLiteReturningRoundTrip exercises Client.QueryWrite against a real
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

	client, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	type returningUser struct {
		ID     int64  `rasql:"id"`
		Email  string `rasql:"email"`
		Status string `rasql:"status"`
	}
	table := schema.Table{
		Name: "returning_users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
			{Name: "status", Type: schema.TypeText, Default: "'pending'"},
		},
		PrimaryKey: []string{"id"},
	}
	users, err := rasql.NewTable[returningUser](table)
	require.NoError(t, err)
	queryUsers := users.QueryTable()
	id, err := queryUsers.Column("id")
	require.NoError(t, err)
	email, err := queryUsers.Column("email")
	require.NoError(t, err)
	status, err := queryUsers.Column("status")
	require.NoError(t, err)
	require.NoError(t, rasql.Create(t.Context(), client, users))

	insert, err := query.NewInsert(queryUsers, []query.Column{email}, []query.Expression{query.Bind("ada@example.com")})
	require.NoError(t, err)
	insert, err = insert.WithReturning(query.Project(id), query.Project(email), query.Project(status))
	require.NoError(t, err)
	inserted, err := rasql.QueryWriteOne[returningUser](t.Context(), client, insert)
	require.NoError(t, err)
	require.Equal(t, returningUser{ID: 1, Email: "ada@example.com", Status: "pending"}, inserted)

	update, err := query.NewUpdate(queryUsers, query.Set(status, query.Bind("active")))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, query.Bind(inserted.ID)))
	require.NoError(t, err)
	update, err = update.WithReturning(query.Project(id), query.Project(email), query.Project(status))
	require.NoError(t, err)
	updated, err := rasql.QueryWriteAll[returningUser](t.Context(), client, update)
	require.NoError(t, err)
	require.Equal(t, []returningUser{{ID: 1, Email: "ada@example.com", Status: "active"}}, updated)

	deleteStatement, err := query.NewDelete(queryUsers)
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(inserted.ID)))
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithReturning(query.Project(id))
	require.NoError(t, err)
	type deletedRow struct {
		ID int64 `rasql:"id"`
	}
	deleted, err := rasql.QueryWriteAll[deletedRow](t.Context(), client, deleteStatement)
	require.NoError(t, err)
	require.Equal(t, []deletedRow{{ID: 1}}, deleted)

	remaining, err := rasql.SelectFrom(client, users).All(t.Context())
	require.NoError(t, err)
	require.Empty(t, remaining)
}
