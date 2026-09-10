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

type roundtripEvent struct {
	ID        int64     `rasql:"id"`
	Active    bool      `rasql:"active"`
	CreatedAt time.Time `rasql:"created_at"`
}

type roundtripEventDecoder struct{ schema rasql.ResultSchema }

func (d roundtripEventDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripEventDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripEventDecoder) DecodeRow(source rasql.ScanSource, row *roundtripEvent) error {
	return source.Scan(&row.ID, &row.Active, &row.CreatedAt)
}

func TestSQLiteTypedSelect(t *testing.T) {
	t.Run("round-trips booleans and times", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		events, err := rasql.TableOf[roundtripEvent](schema.TableDef{
			Name: "events",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "active", Type: schema.BooleanType{}},
				{Name: "created_at", Type: schema.TimeType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, events))

		relation, err := rasql.SourceOf(events, "")
		require.NoError(t, err)
		eventID, err := rasql.BindColumn[roundtripEvent, int64](relation, "id", "")
		require.NoError(t, err)
		eventActive, err := rasql.BindColumn[roundtripEvent, bool](relation, "active", "")
		require.NoError(t, err)
		eventCreatedAt, err := rasql.BindColumn[roundtripEvent, time.Time](relation, "created_at", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "active", Type: schema.BooleanType{}},
			rasql.ResultColumn{Name: "created_at", Type: schema.TimeType{}},
		)
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", eventID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("active", eventActive.Expr(), schema.BooleanType{}, ""),
			rasql.Item("created_at", eventCreatedAt.Expr(), schema.TimeType{}, ""),
		}, roundtripEventDecoder{schema: resultSchema})
		require.NoError(t, err)

		expected := roundtripEvent{
			ID:        42,
			Active:    true,
			CreatedAt: time.Date(2026, time.August, 1, 12, 30, 45, 123456789, time.UTC),
		}
		createPlan, err := rasql.NewCreatePlan(events,
			rasql.SetField(eventID, expected.ID),
			rasql.SetField(eventActive, expected.Active),
			rasql.SetField(eventCreatedAt, expected.CreatedAt),
		)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, createPlan)
		require.NoError(t, err)

		actual, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), projection).
			Where(rasql.EqualValue(eventID.Expr(), expected.ID)))
		require.NoError(t, err)
		require.Equal(t, expected, actual)

		_, err = rasql.One(t.Context(), executor, rasql.Select(relation.Source(), projection).
			Where(rasql.EqualValue(eventID.Expr(), expected.ID+1)))
		require.ErrorIs(t, err, rasql.ErrNoRows)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("dispatches NULL to scanner fields", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		records, err := rasql.TableOf[roundtripRecord](schema.TableDef{
			Name: "records",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "note", Type: schema.TextType{}, Nullable: true},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, records))
		_, err = database.ExecContext(t.Context(), `INSERT INTO records (id, note) VALUES (1, NULL)`)
		require.NoError(t, err)

		relation, err := rasql.SourceOf(records, "")
		require.NoError(t, err)
		recordID, err := rasql.BindColumn[roundtripRecord, int64](relation, "id", "")
		require.NoError(t, err)
		recordNote, err := rasql.BindNullColumn[roundtripRecord, string](relation, "note", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "note", Type: schema.TextType{}, Nullable: true},
		)
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", recordID.Expr(), schema.IntegerType{}, ""),
			rasql.NullItem("note", recordNote.NullExpr(), schema.TextType{}, ""),
		}, roundtripRecordDecoder{schema: resultSchema})
		require.NoError(t, err)

		actual, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), projection).
			Where(rasql.EqualValue(recordID.Expr(), int64(1))))
		require.NoError(t, err)
		require.False(t, actual.Note.Valid)
		require.Empty(t, actual.Note.String)
	})

	t.Run("WHERE IN filters rows", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		users, err := rasql.TableOf[roundtripUser](schema.TableDef{
			Name: "users",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "email", Type: schema.TextType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, users))

		relation, err := rasql.SourceOf(users, "")
		require.NoError(t, err)
		userID, err := rasql.BindColumn[roundtripUser, int64](relation, "id", "")
		require.NoError(t, err)
		userEmail, err := rasql.BindColumn[roundtripUser, string](relation, "email", "")
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", userID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("email", userEmail.Expr(), schema.TextType{}, ""),
		}, roundtripUserDecoder{schema: roundtripUserResultSchema(t)})
		require.NoError(t, err)

		inserted := []roundtripUser{
			{ID: 1, Email: "ada@example.com"},
			{ID: 2, Email: "bob@example.com"},
			{ID: 3, Email: "cyd@example.com"},
		}
		for _, row := range inserted {
			plan, err := rasql.NewCreatePlan(users, rasql.SetField(userID, row.ID), rasql.SetField(userEmail, row.Email))
			require.NoError(t, err)
			_, err = rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
		}

		actual, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), projection).
			Where(rasql.InValues(userID.Expr(), inserted[0].ID, inserted[2].ID)).
			OrderBy(rasql.AscExpr(userID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []roundtripUser{inserted[0], inserted[2]}, actual)
	})

	// TestSQLiteTypedSelect/"a subquery filters rows" runs InQuery against a real SQLite
	// database: it keeps users who placed a high-value order.
	//
	// The old test also ran the same restriction as a scalar subquery, comparing
	// each order's amount against an averaging subquery's result with >=. That
	// half is not converted: the canonical API's typed Expr[T]/Predicate layer
	// has no way to lift an arbitrary query.Expression such as query.Scalar(average)
	// into an Expr[T] usable on either side of GreaterOrEqualExpr, unlike a
	// boolean subquery position, which ExistsQuery/InQuery/NotExistsQuery/
	// NotInQuery do cover.
	t.Run("a subquery filters rows", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		users, err := rasql.TableOf[roundtripUser](schema.TableDef{
			Name: "users",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "email", Type: schema.TextType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, users))

		usersRelation, err := rasql.SourceOf(users, "")
		require.NoError(t, err)
		userID, err := rasql.BindColumn[roundtripUser, int64](usersRelation, "id", "")
		require.NoError(t, err)
		userEmail, err := rasql.BindColumn[roundtripUser, string](usersRelation, "email", "")
		require.NoError(t, err)
		userProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", userID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("email", userEmail.Expr(), schema.TextType{}, ""),
		}, roundtripUserDecoder{schema: roundtripUserResultSchema(t)})
		require.NoError(t, err)

		orders, err := rasql.TableOf[roundtripOrder](schema.TableDef{
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "user_id", Type: schema.IntegerType{}},
				{Name: "amount", Type: schema.IntegerType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, orders))
		ordersRelation, err := rasql.SourceOf(orders, "")
		require.NoError(t, err)
		orderID, err := rasql.BindColumn[roundtripOrder, int64](ordersRelation, "id", "")
		require.NoError(t, err)
		orderUserID, err := rasql.BindColumn[roundtripOrder, int64](ordersRelation, "user_id", "")
		require.NoError(t, err)
		orderAmount, err := rasql.BindColumn[roundtripOrder, int64](ordersRelation, "amount", "")
		require.NoError(t, err)

		insertedUsers := []roundtripUser{
			{ID: 1, Email: "ada@example.com"},
			{ID: 2, Email: "bob@example.com"},
			{ID: 3, Email: "cyd@example.com"},
		}
		for _, row := range insertedUsers {
			plan, err := rasql.NewCreatePlan(users, rasql.SetField(userID, row.ID), rasql.SetField(userEmail, row.Email))
			require.NoError(t, err)
			_, err = rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
		}
		for _, row := range []roundtripOrder{
			{ID: 1, UserID: 1, Amount: 80},
			{ID: 2, UserID: 2, Amount: 20},
			{ID: 3, UserID: 3, Amount: 100},
		} {
			plan, err := rasql.NewCreatePlan(orders,
				rasql.SetField(orderID, row.ID), rasql.SetField(orderUserID, row.UserID), rasql.SetField(orderAmount, row.Amount))
			require.NoError(t, err)
			_, err = rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
		}

		highSpenderUserID, err := rasql.Scalar("user_id", orderUserID.Expr(), schema.IntegerType{}, "")
		require.NoError(t, err)
		highSpenders := rasql.Select(ordersRelation.Source(), highSpenderUserID).
			Where(rasql.GreaterValue(orderAmount.Expr(), int64(50)))

		inHighSpenders, err := rasql.InQuery(userID.Expr(), highSpenders)
		require.NoError(t, err)
		viaInSelect, err := rasql.All(t.Context(), executor, rasql.Select(usersRelation.Source(), userProjection).
			Where(inHighSpenders).
			OrderBy(rasql.AscExpr(userID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []roundtripUser{insertedUsers[0], insertedUsers[2]}, viaInSelect)
	})

	// TestSQLiteTypedSelect/"scalar functions filter rows" runs LOWER against a real
	// SQLite database: LOWER(email) matches a mixed-case row against a
	// lower-case bound value.
	//
	// The old test also ran COALESCE(score, 0) as a predicate and as a projected
	// value. Neither half is converted: query_api.go, expression.go and
	// expression_operators.go have no typed COALESCE wrapper (nothing analogous
	// to LowerExpr/UpperExpr for it), so there is no way to reach it except the
	// same raw query.Expression escape hatch dynamic and query_render tests use,
	// which would test string-built SQL rather than the typed operator surface
	// this file is about.
	t.Run("scalar functions filter rows", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		users, err := rasql.TableOf[roundtripScoredUser](schema.TableDef{
			Name: "users",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "email", Type: schema.TextType{}},
				{Name: "score", Type: schema.IntegerType{}, Nullable: true},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, users))

		relation, err := rasql.SourceOf(users, "")
		require.NoError(t, err)
		userID, err := rasql.BindColumn[roundtripScoredUser, int64](relation, "id", "")
		require.NoError(t, err)
		email, err := rasql.BindColumn[roundtripScoredUser, string](relation, "email", "")
		require.NoError(t, err)
		score, err := rasql.BindNullColumn[roundtripScoredUser, int64](relation, "score", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
			rasql.ResultColumn{Name: "score", Type: schema.IntegerType{}, Nullable: true},
		)
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", userID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("email", email.Expr(), schema.TextType{}, ""),
			rasql.NullItem("score", score.NullExpr(), schema.IntegerType{}, ""),
		}, roundtripScoredUserDecoder{schema: resultSchema})
		require.NoError(t, err)

		ten := int64(10)
		inserted := []roundtripScoredUser{
			{ID: 1, Email: "Ada@Example.com", Score: &ten},
			{ID: 2, Email: "bob@example.com", Score: nil},
		}
		for _, row := range inserted {
			fields := []rasql.MutationField[roundtripScoredUser]{
				rasql.SetField(userID, row.ID),
				rasql.SetField(email, row.Email),
			}
			if row.Score != nil {
				fields = append(fields, rasql.SetNullableField(score, *row.Score))
			} else {
				fields = append(fields, rasql.ClearField(score))
			}
			plan, err := rasql.NewCreatePlan(users, fields...)
			require.NoError(t, err)
			_, err = rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
		}

		byLowerEmail, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), projection).
			Where(rasql.EqualValue(rasql.LowerExpr(email.Expr()), "ada@example.com")))
		require.NoError(t, err)
		require.Equal(t, []roundtripScoredUser{inserted[0]}, byLowerEmail)
	})

	t.Run("counts rows", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		events, err := rasql.TableOf[roundtripCountedEvent](schema.TableDef{
			Name: "events",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "active", Type: schema.BooleanType{}},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, events))

		relation, err := rasql.SourceOf(events, "")
		require.NoError(t, err)
		eventID, err := rasql.BindColumn[roundtripCountedEvent, int64](relation, "id", "")
		require.NoError(t, err)
		eventActive, err := rasql.BindColumn[roundtripCountedEvent, bool](relation, "active", "")
		require.NoError(t, err)

		for _, row := range []roundtripCountedEvent{
			{ID: 1, Active: true},
			{ID: 2, Active: true},
			{ID: 3, Active: false},
		} {
			plan, err := rasql.NewCreatePlan(events, rasql.SetField(eventID, row.ID), rasql.SetField(eventActive, row.Active))
			require.NoError(t, err)
			_, err = rasql.ExecMutation(t.Context(), executor, plan)
			require.NoError(t, err)
		}

		countProjection, err := rasql.Scalar("count", rasql.CountRows(), schema.IntegerType{}, "")
		require.NoError(t, err)

		total, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), countProjection))
		require.NoError(t, err)
		require.Equal(t, int64(3), total)

		active, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), countProjection).
			Where(rasql.EqualValue(eventActive.Expr(), true)))
		require.NoError(t, err)
		require.Equal(t, int64(2), active)

		// Two predicates must both reach the counted statement, so the count has
		// to drop the inactive row and the second active row alike.
		activeFirst, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), countProjection).
			Where(rasql.And(rasql.EqualValue(eventActive.Expr(), true), rasql.EqualValue(eventID.Expr(), int64(1)))))
		require.NoError(t, err)
		require.Equal(t, int64(1), activeFirst)
	})
}

type roundtripRecord struct {
	ID   int64          `rasql:"id"`
	Note sql.NullString `rasql:"note"`
}

type roundtripRecordDecoder struct{ schema rasql.ResultSchema }

func (d roundtripRecordDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripRecordDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripRecordDecoder) DecodeRow(source rasql.ScanSource, row *roundtripRecord) error {
	return source.Scan(&row.ID, &row.Note)
}

type roundtripUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

type roundtripUserDecoder struct{ schema rasql.ResultSchema }

func (d roundtripUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripUserDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripUserDecoder) DecodeRow(source rasql.ScanSource, row *roundtripUser) error {
	return source.Scan(&row.ID, &row.Email)
}

func roundtripUserResultSchema(t *testing.T) rasql.ResultSchema {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	return resultSchema
}

type roundtripOrder struct {
	ID     int64 `rasql:"id"`
	UserID int64 `rasql:"user_id"`
	Amount int64 `rasql:"amount"`
}

type roundtripScoredUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
	Score *int64 `rasql:"score"`
}

type roundtripScoredUserDecoder struct{ schema rasql.ResultSchema }

func (d roundtripScoredUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripScoredUserDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripScoredUserDecoder) DecodeRow(source rasql.ScanSource, row *roundtripScoredUser) error {
	return source.Scan(&row.ID, &row.Email, &row.Score)
}

type roundtripCountedEvent struct {
	ID     int64 `rasql:"id"`
	Active bool  `rasql:"active"`
}

// TestSQLiteGeneratedRowMethodsRoundTrip is not converted. Its subject was the
// old reflective decode fallback: a row type with no ScanRow method still
// round-tripped through SelectFrom, because the field-mapping decoder
// snake-cased its untagged field names to column names on its own, and
// ColumnValue supplied Insert's write side the same way. The canonical typed
// Select has no such fallback: every projection needs an explicit RowDecoder
// (ResultSchema/Presence/DecodeRow) built from real bound columns, the way
// every other test in this file now does it, and there is no path left that
// exercises "no ScanRow method is needed." Writing this test with a
// hand-written decoder would only duplicate
// TestSQLiteTypedSelect/"round-trips booleans and times"'s coverage without proving
// the claim its name makes.

// TestSQLiteRoundTrip/"a decimal round-trips exactly" is the test that would have caught the
// NUMERIC(19,4)-to-REAL truncation change 2 documents: SQLite has no exact
// decimal storage class, so a DecimalType column is declared TEXT and the
// inserted digits must come back byte-identical rather than rounded through
// float64. Byte-identical is a property of SQLite's TEXT storage, not of the
// decimal type: PostgreSQL and MySQL return a decimal in its column's
// declared scale, zero-padded on the right, which TestDatabaseIntegration
// pins against the live servers.
type roundtripInvoice struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

type roundtripInvoiceDecoder struct{ schema rasql.ResultSchema }

func (d roundtripInvoiceDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripInvoiceDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripInvoiceDecoder) DecodeRow(source rasql.ScanSource, row *roundtripInvoice) error {
	return source.Scan(&row.ID, &row.Amount)
}

func TestSQLiteRoundTrip(t *testing.T) {
	t.Run("a decimal round-trips exactly", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		decimalType := schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}
		invoices, err := rasql.TableOf[roundtripInvoice](schema.TableDef{
			Name: "invoices",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "amount", Type: decimalType},
			},
			PrimaryKey: []string{"id"},
		})
		require.NoError(t, err)
		require.NoError(t, rasql.CreateTable(t.Context(), db, invoices))

		relation, err := rasql.SourceOf(invoices, "")
		require.NoError(t, err)
		invoiceID, err := rasql.BindColumn[roundtripInvoice, int64](relation, "id", "")
		require.NoError(t, err)
		invoiceAmount, err := rasql.BindColumn[roundtripInvoice, string](relation, "amount", "")
		require.NoError(t, err)
		resultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "amount", Type: decimalType},
		)
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", invoiceID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("amount", invoiceAmount.Expr(), decimalType, ""),
		}, roundtripInvoiceDecoder{schema: resultSchema})
		require.NoError(t, err)

		expected := roundtripInvoice{ID: 1, Amount: "1234.5678901234567890"}
		plan, err := rasql.NewCreatePlan(invoices, rasql.SetField(invoiceID, expected.ID), rasql.SetField(invoiceAmount, expected.Amount))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)

		actual, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), projection).
			Where(rasql.EqualValue(invoiceID.Expr(), expected.ID)))
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	})

	t.Run("a qualified table", func(t *testing.T) {
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
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)

		events, err := rasql.TableOf[roundtripQualifiedEvent](schema.TableDef{
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
		id := queryEvents.Column("id")
		userIDColumn := queryEvents.Column("user_id")
		action := queryEvents.Column("action")

		relation, err := rasql.SourceOf(events, "")
		require.NoError(t, err)
		eventID, err := rasql.BindColumn[roundtripQualifiedEvent, int64](relation, "id", "")
		require.NoError(t, err)
		eventUserID, err := rasql.BindColumn[roundtripQualifiedEvent, int64](relation, "user_id", "")
		require.NoError(t, err)
		eventAction, err := rasql.BindColumn[roundtripQualifiedEvent, string](relation, "action", "")
		require.NoError(t, err)
		eventResultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "action", Type: schema.TextType{}},
		)
		require.NoError(t, err)
		eventProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", eventID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("user_id", eventUserID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("action", eventAction.Expr(), schema.TextType{}, ""),
		}, roundtripQualifiedEventDecoder{schema: eventResultSchema})
		require.NoError(t, err)

		// Multi-row INSERT into the qualified table, built directly against the
		// query package -- multi-row insert has no typed-plan equivalent, so this
		// stays exactly as it was and only the execution changes.
		insertRows, err := query.NewInsertRows(queryEvents, []query.ColumnRef{id, userIDColumn, action}, [][]any{
			{int64(1), int64(10), "created"},
			{int64(2), int64(10), "updated"},
			{int64(3), int64(11), "created"},
		})
		require.NoError(t, err)
		insertPlan, err := rasql.NewStatementPlan(insertRows)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, insertPlan)
		require.NoError(t, err)

		// SELECT with a qualified predicate.
		byUser, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), eventProjection).
			Where(rasql.EqualValue(eventUserID.Expr(), int64(10))).
			OrderBy(rasql.AscExpr(eventID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []roundtripQualifiedEvent{
			{ID: 1, UserID: 10, Action: "created"},
			{ID: 2, UserID: 10, Action: "updated"},
		}, byUser)

		// A grouped projection over the qualified table.
		countResultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "user_id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}},
		)
		require.NoError(t, err)
		countProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("user_id", eventUserID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("total", rasql.CountRows(), schema.IntegerType{}, ""),
		}, roundtripUserEventCountDecoder{schema: countResultSchema})
		require.NoError(t, err)
		grouped, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), countProjection).
			GroupBy(rasql.Group(eventUserID.Expr())).
			OrderBy(rasql.AscExpr(eventUserID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []roundtripUserEventCount{
			{UserID: 10, Total: 2},
			{UserID: 11, Total: 1},
		}, grouped)

		// A subquery naming the qualified table, both as the outer and inner
		// statement.
		prolificUserID, err := rasql.Scalar("user_id", eventUserID.Expr(), schema.IntegerType{}, "")
		require.NoError(t, err)
		prolific := rasql.Select(relation.Source(), prolificUserID).
			GroupBy(rasql.Group(eventUserID.Expr())).
			Having(rasql.GreaterValue(rasql.CountRows(), int64(1)))
		inProlific, err := rasql.InQuery(eventUserID.Expr(), prolific)
		require.NoError(t, err)
		viaSubquery, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), eventProjection).
			Where(inProlific).
			OrderBy(rasql.AscExpr(eventID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []roundtripQualifiedEvent{
			{ID: 1, UserID: 10, Action: "created"},
			{ID: 2, UserID: 10, Action: "updated"},
		}, viaSubquery)

		// UPDATE against the qualified table, with a qualified predicate. Built
		// directly against the query package, exactly as before; only the
		// execution changes.
		update, err := query.NewUpdate(queryEvents, query.Set(action, query.Bind("closed")))
		require.NoError(t, err)
		update, err = update.WithWhere(query.Equal(id, query.Bind(int64(1))))
		require.NoError(t, err)
		updatePlan, err := rasql.NewStatementPlan(update)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, updatePlan)
		require.NoError(t, err)

		updated, err := rasql.One(t.Context(), executor, rasql.Select(relation.Source(), eventProjection).
			Where(rasql.EqualValue(eventID.Expr(), int64(1))))
		require.NoError(t, err)
		require.Equal(t, "closed", updated.Action)

		// DELETE against the qualified table, with a qualified predicate.
		deleteStatement, err := query.NewDelete(queryEvents)
		require.NoError(t, err)
		deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(int64(3))))
		require.NoError(t, err)
		deletePlan, err := rasql.NewStatementPlan(deleteStatement)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, deletePlan)
		require.NoError(t, err)

		remaining, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), eventProjection).
			OrderBy(rasql.AscExpr(eventID.Expr())))
		require.NoError(t, err)
		require.Equal(t, []roundtripQualifiedEvent{
			{ID: 1, UserID: 10, Action: "closed"},
			{ID: 2, UserID: 10, Action: "updated"},
		}, remaining)
	})

	// TestSQLiteRoundTrip/"RETURNING" exercises rasql.Returning against a real
	// database: an INSERT reads back a database-assigned id and a defaulted
	// column through One, then an UPDATE and a DELETE each read back their
	// affected rows through All.
	t.Run("RETURNING", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, database.Close())
		})
		database.SetMaxOpenConns(1)

		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		table := schema.TableDef{
			Name: "returning_users",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "email", Type: schema.TextType{}},
				{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
			},
			PrimaryKey: []string{"id"},
		}
		users, err := rasql.TableOf[roundtripReturningUser](table)
		require.NoError(t, err)
		queryUsers := users.Ref()
		id := queryUsers.Column("id")
		email := queryUsers.Column("email")
		status := queryUsers.Column("status")
		require.NoError(t, rasql.CreateTable(t.Context(), db, users))

		relation, err := rasql.SourceOf(users, "")
		require.NoError(t, err)
		userID, err := rasql.BindColumn[roundtripReturningUser, int64](relation, "id", "")
		require.NoError(t, err)
		userEmail, err := rasql.BindColumn[roundtripReturningUser, string](relation, "email", "")
		require.NoError(t, err)
		userStatus, err := rasql.BindColumn[roundtripReturningUser, string](relation, "status", "")
		require.NoError(t, err)
		userResultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
			rasql.ResultColumn{Name: "status", Type: schema.TextType{}},
		)
		require.NoError(t, err)
		userProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", userID.Expr(), schema.IntegerType{}, ""),
			rasql.Item("email", userEmail.Expr(), schema.TextType{}, ""),
			rasql.Item("status", userStatus.Expr(), schema.TextType{}, ""),
		}, roundtripReturningUserDecoder{schema: userResultSchema})
		require.NoError(t, err)

		insert, err := query.NewInsert(queryUsers, query.Set(email, "ada@example.com"))
		require.NoError(t, err)
		insertPlan, err := rasql.NewStatementPlan(insert)
		require.NoError(t, err)
		insertQuery, err := rasql.Returning(insertPlan, userProjection)
		require.NoError(t, err)
		inserted, err := rasql.One(t.Context(), executor, insertQuery)
		require.NoError(t, err)
		require.Equal(t, roundtripReturningUser{ID: 1, Email: "ada@example.com", Status: "pending"}, inserted)

		update, err := query.NewUpdate(queryUsers, query.Set(status, query.Bind("active")))
		require.NoError(t, err)
		update, err = update.WithWhere(query.Equal(id, query.Bind(inserted.ID)))
		require.NoError(t, err)
		updatePlan, err := rasql.NewStatementPlan(update)
		require.NoError(t, err)
		updateQuery, err := rasql.Returning(updatePlan, userProjection)
		require.NoError(t, err)
		updated, err := rasql.All(t.Context(), executor, updateQuery)
		require.NoError(t, err)
		require.Equal(t, []roundtripReturningUser{{ID: 1, Email: "ada@example.com", Status: "active"}}, updated)

		deletedResultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		deletedProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("id", userID.Expr(), schema.IntegerType{}, ""),
		}, roundtripDeletedRowDecoder{schema: deletedResultSchema})
		require.NoError(t, err)

		deleteStatement, err := query.NewDelete(queryUsers)
		require.NoError(t, err)
		deleteStatement, err = deleteStatement.WithWhere(query.Equal(id, query.Bind(inserted.ID)))
		require.NoError(t, err)
		deletePlan, err := rasql.NewStatementPlan(deleteStatement)
		require.NoError(t, err)
		deleteQuery, err := rasql.Returning(deletePlan, deletedProjection)
		require.NoError(t, err)
		deleted, err := rasql.All(t.Context(), executor, deleteQuery)
		require.NoError(t, err)
		require.Equal(t, []roundtripDeletedRow{{ID: 1}}, deleted)

		remaining, err := rasql.All(t.Context(), executor, rasql.Select(relation.Source(), userProjection))
		require.NoError(t, err)
		require.Empty(t, remaining)
	})
}

// TestSQLiteRoundTrip/"a qualified table" runs select, join, group, subquery,
// multi-row insert, update and delete against a schema-qualified table over
// a real SQLite database with a second database attached, pinning the
// rendered text against a real parser rather than a golden string. The
// qualified table is created through rasql.CreateTable itself, which now renders
// CREATE TABLE into the named database rather than dropping the qualifier;
// only the attached database's existence stands in for a reviewed native
// migration, the same way a native migration creates a PostgreSQL schema or
// a MySQL database in production.
type roundtripQualifiedEvent struct {
	ID     int64  `rasql:"id"`
	UserID int64  `rasql:"user_id"`
	Action string `rasql:"action"`
}

type roundtripQualifiedEventDecoder struct{ schema rasql.ResultSchema }

func (d roundtripQualifiedEventDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripQualifiedEventDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripQualifiedEventDecoder) DecodeRow(source rasql.ScanSource, row *roundtripQualifiedEvent) error {
	return source.Scan(&row.ID, &row.UserID, &row.Action)
}

type roundtripUserEventCount struct {
	UserID int64 `rasql:"user_id"`
	Total  int64 `rasql:"total"`
}

type roundtripUserEventCountDecoder struct{ schema rasql.ResultSchema }

func (d roundtripUserEventCountDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripUserEventCountDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripUserEventCountDecoder) DecodeRow(source rasql.ScanSource, row *roundtripUserEventCount) error {
	return source.Scan(&row.UserID, &row.Total)
}

type roundtripReturningUser struct {
	ID     int64  `rasql:"id"`
	Email  string `rasql:"email"`
	Status string `rasql:"status"`
}

type roundtripReturningUserDecoder struct{ schema rasql.ResultSchema }

func (d roundtripReturningUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripReturningUserDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripReturningUserDecoder) DecodeRow(source rasql.ScanSource, row *roundtripReturningUser) error {
	return source.Scan(&row.ID, &row.Email, &row.Status)
}

type roundtripDeletedRow struct {
	ID int64 `rasql:"id"`
}

type roundtripDeletedRowDecoder struct{ schema rasql.ResultSchema }

func (d roundtripDeletedRowDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (roundtripDeletedRowDecoder) Presence() []rasql.Presence         { return nil }
func (d roundtripDeletedRowDecoder) DecodeRow(source rasql.ScanSource, row *roundtripDeletedRow) error {
	return source.Scan(&row.ID)
}
