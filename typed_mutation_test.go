package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// usersRowProjection reads store.UsersRow back through the same table
// store.Users() names, using the projection the generator wrote for it --
// DynamicProjection does not fit here: it decodes an existing result set, but
// a RETURNING projection needs a real expression for every column it names,
// and DynamicProjection's items are that column's name projecting a bound
// NULL, not the column itself.
func usersRowProjection(t *testing.T) rasql.Projection[store.UsersRow] {
	t.Helper()

	columns, err := (store.UsersColumns{}).Bind(store.Users().Table)
	require.NoError(t, err)
	projection, err := store.UsersProjection(columns)
	require.NoError(t, err)
	return projection
}

func TestTypedMutationPlans(t *testing.T) {
	t.Run("generated plans persist defaults and presence", func(t *testing.T) {
		database, err := sql.Open("sqlite", "file:typed_mutation?mode=memory&cache=shared")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(), `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		nickname TEXT,
		status TEXT NOT NULL DEFAULT 'pending',
		first_name TEXT NOT NULL,
		last_name TEXT NOT NULL
	)`)
		require.NoError(t, err)
		executor, err := rasql.Open(t.Context(), database, dialect.SQLite())
		require.NoError(t, err)
		projection := usersRowProjection(t)

		createPlan, err := store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
		require.NoError(t, err)
		createQuery, err := rasql.Returning(createPlan, projection)
		require.NoError(t, err)
		created, err := rasql.One(t.Context(), executor, createQuery)
		require.NoError(t, err)
		require.Equal(t, int64(1), created.ID)
		require.Equal(t, "pending", created.Status)
		patch, err := store.NewUsersPatch().Status("active").Where(queryEqualID(created.ID))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, patch)
		require.NoError(t, err)
		patch, err = store.NewUsersPatch().ClearNickname().Where(queryEqualID(created.ID))
		require.NoError(t, err)
		patchQuery, err := rasql.Returning(patch, projection)
		require.NoError(t, err)
		updated, err := rasql.One(t.Context(), executor, patchQuery)
		require.NoError(t, err)
		require.Equal(t, "active", updated.Status)
		require.False(t, updated.Nickname.Valid)
	})

	t.Run("generated-column plans", func(t *testing.T) {
		table, executor, mock, cleanup := generatedMeasurementTable(t)
		t.Cleanup(cleanup)
		relation := table
		id, err := rasql.BindColumn[generatedMeasurement, int64](relation, "id", "")
		require.NoError(t, err)
		celsius, err := rasql.BindColumn[generatedMeasurement, int64](relation, "celsius", "")
		require.NoError(t, err)
		fahrenheit, err := rasql.BindColumn[generatedMeasurement, int64](relation, "fahrenheit", "")
		require.NoError(t, err)

		create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(celsius, int64(20)))
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO \"measurements\" (\"id\", \"celsius\") VALUES ($1, $2)").WithArgs(int64(1), int64(20)).WillReturnResult(sqlmock.NewResult(1, 1))
		_, err = rasql.ExecMutation(t.Context(), executor, create)
		require.NoError(t, err)

		patch, err := rasql.NewPatchPlan(table, rasql.EqualExpr(id.Expr(), rasql.Value(int64(1))), rasql.SetField(celsius, int64(21)))
		require.NoError(t, err)
		mock.ExpectExec("UPDATE \"measurements\" SET \"celsius\" = $1 WHERE (\"measurements\".\"id\" = $2)").WithArgs(int64(21), int64(1)).WillReturnResult(sqlmock.NewResult(1, 1))
		_, err = rasql.ExecMutation(t.Context(), executor, patch)
		require.NoError(t, err)

		invalid, err := rasql.NewCreatePlan(table, rasql.SetField(fahrenheit, int64(68)))
		require.ErrorContains(t, err, "not writable")
		_ = invalid
	})
}

func queryEqualID(id int64) rasql.Predicate {
	columns, err := (store.UsersColumns{}).Bind(store.Users().Table)
	if err != nil {
		panic(err)
	}
	return rasql.EqualValue(columns.ID.Expr(), id)
}

type mutationValidationRow struct{}

// noCallExecutor stands in for rasql.DB{} and noCallHandle in the old API: a
// terminal that reaches it before a construction error is caught is a bug, so
// every method panics.
type noCallExecutor struct{}

func (noCallExecutor) Dialect() dialect.Dialect { panic("Dialect must not be called") }
func (noCallExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	panic("Query must not be called")
}
func (noCallExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	panic("Exec must not be called")
}

type mutationValidationDecoder struct{ schema rasql.ResultSchema }

func (d mutationValidationDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (mutationValidationDecoder) Presence() []rasql.Presence         { return nil }
func (d mutationValidationDecoder) DecodeRow(source rasql.ScanSource, _ *mutationValidationRow) error {
	var discard int64
	return source.Scan(&discard)
}

// mutationValidationProjection is a minimal valid Projection[mutationValidationRow],
// needed because rasql.Returning refuses a zero projection before it ever
// looks at the plan; every sticky-error case below needs one just to reach
// the plan's own stored error.
func mutationValidationProjection(t *testing.T, first rasql.Table[mutationValidationRow]) rasql.Projection[mutationValidationRow] {
	t.Helper()

	relation := first
	id, err := rasql.BindColumn[mutationValidationRow, int64](relation, "id", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
	}, mutationValidationDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func mutationValidationTables() (rasql.Table[mutationValidationRow], rasql.Table[mutationValidationRow]) {
	first := rasql.MustTableOf[mutationValidationRow](schema.TableDef{
		Name:       "first",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}, Default: "'new'"},
			{Name: "optional", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKeyAutoincrement: true,
	})
	second := rasql.MustTableOf[mutationValidationRow](schema.TableDef{
		Name:       "second",
		PrimaryKey: []string{"id"},
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
	})
	return first, second
}

func TestTypedMutationPlanValidation(t *testing.T) {
	t.Run("validation errors and sticky terminals", func(t *testing.T) {
		first, second := mutationValidationTables()
		id := query.TypedColumnOf[mutationValidationRow, int64](first.Column("id"))
		name := query.TypedColumnOf[mutationValidationRow, string](first.Column("name"))
		otherID := query.TypedColumnOf[mutationValidationRow, int64](second.Column("id"))

		var zeroField rasql.MutationField[mutationValidationRow]
		var nilTable rasql.Table[mutationValidationRow]
		validPredicate := query.EqualValue(id, int64(1))
		tests := []struct {
			name string
			make func() error
			want string
		}{
			{"nil table", func() error { _, err := rasql.NewCreatePlan(nilTable, rasql.SetField(name, "x")); return err }, "table must not be nil"},
			{"empty create", func() error { _, err := rasql.NewCreatePlan(first); return err }, "at least one field"},
			{"zero field", func() error { _, err := rasql.NewCreatePlan(first, zeroField); return err }, "zero field"},
			{"duplicate field", func() error {
				_, err := rasql.NewCreatePlan(first, rasql.SetField(name, "a"), rasql.SetField(name, "b"))
				return err
			}, "duplicate column"},
			{"cross table field", func() error { _, err := rasql.NewCreatePlan(first, rasql.SetField(otherID, int64(1))); return err }, "belongs to another table"},
			{"zero predicate", func() error {
				_, err := rasql.NewPatchPlan(first, query.Predicate{}, rasql.SetField(name, "x"))
				return err
			}, "requires a predicate"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				require.ErrorContains(t, test.make(), test.want)
			})
		}

		projection := mutationValidationProjection(t, first)

		plan, err := rasql.NewCreatePlan(first, rasql.SetField(name, "x"), rasql.SetField(name, "y"))
		require.Error(t, err)
		_, terminalErr := rasql.ExecMutation(context.Background(), noCallExecutor{}, plan)
		require.EqualError(t, terminalErr, err.Error(), "constructor errors remain sticky through terminals")

		patchPlan, err := rasql.NewPatchPlan(first, validPredicate, rasql.SetField(name, "x"))
		require.NoError(t, err)
		patchQuery, terminalErr := rasql.Returning(patchPlan, projection)
		require.NoError(t, terminalErr, "a valid plan builds a valid RETURNING query")
		// The plan is valid; what must still fail gracefully, rather than panic,
		// is handing Open an invalid handle -- nil in place of the old zero
		// rasql.DB.
		_, terminalErr = rasql.Open(context.Background(), nil, dialect.MySQL(), rasql.WithProfile(rasql.MySQL84()))
		require.Error(t, terminalErr)
		_ = patchQuery

		createErrors := []struct {
			name string
			plan rasql.CreatePlan[mutationValidationRow]
			err  error
		}{}
		createPlan, createErr := rasql.NewCreatePlan(nilTable, rasql.SetField(name, "x"))
		createErrors = append(createErrors, struct {
			name string
			plan rasql.CreatePlan[mutationValidationRow]
			err  error
		}{"nil table", createPlan, createErr})
		createPlan, createErr = rasql.NewCreatePlan(first)
		createErrors = append(createErrors, struct {
			name string
			plan rasql.CreatePlan[mutationValidationRow]
			err  error
		}{"empty create", createPlan, createErr})
		createPlan, createErr = rasql.NewCreatePlan(first, zeroField)
		createErrors = append(createErrors, struct {
			name string
			plan rasql.CreatePlan[mutationValidationRow]
			err  error
		}{"zero create", createPlan, createErr})
		createPlan, createErr = rasql.NewCreatePlan(first, rasql.SetField(name, "a"), rasql.SetField(name, "b"))
		createErrors = append(createErrors, struct {
			name string
			plan rasql.CreatePlan[mutationValidationRow]
			err  error
		}{"duplicate create", createPlan, createErr})
		createPlan, createErr = rasql.NewCreatePlan(first, rasql.SetField(otherID, int64(1)))
		createErrors = append(createErrors, struct {
			name string
			plan rasql.CreatePlan[mutationValidationRow]
			err  error
		}{"cross table create", createPlan, createErr})
		for _, test := range createErrors {
			t.Run("sticky create "+test.name, func(t *testing.T) {
				require.Error(t, test.err)
				_, got := rasql.ExecMutation(context.Background(), noCallExecutor{}, test.plan)
				require.EqualError(t, got, test.err.Error())
			})
		}

		patchErrors := []struct {
			name string
			plan rasql.PatchPlan[mutationValidationRow]
			err  error
		}{}
		patchPlan, patchErr := rasql.NewPatchPlan(first, validPredicate)
		patchErrors = append(patchErrors, struct {
			name string
			plan rasql.PatchPlan[mutationValidationRow]
			err  error
		}{"empty patch", patchPlan, patchErr})
		patchPlan, patchErr = rasql.NewPatchPlan(first, validPredicate, zeroField)
		patchErrors = append(patchErrors, struct {
			name string
			plan rasql.PatchPlan[mutationValidationRow]
			err  error
		}{"zero patch", patchPlan, patchErr})
		patchPlan, patchErr = rasql.NewPatchPlan(first, validPredicate, rasql.SetField(otherID, int64(1)))
		patchErrors = append(patchErrors, struct {
			name string
			plan rasql.PatchPlan[mutationValidationRow]
			err  error
		}{"cross table patch", patchPlan, patchErr})
		patchPlan, patchErr = rasql.NewPatchPlan(first, query.Predicate{}, rasql.SetField(name, "x"))
		patchErrors = append(patchErrors, struct {
			name string
			plan rasql.PatchPlan[mutationValidationRow]
			err  error
		}{"zero predicate patch", patchPlan, patchErr})
		for _, test := range patchErrors {
			t.Run("sticky patch "+test.name, func(t *testing.T) {
				require.Error(t, test.err)
				// Returning alone reaches the plan's own mutationPlan() and its
				// stored error, with no executor and no database involved at all,
				// which is a strictly stronger version of "the terminal never
				// touches the database" than the old QueryPatchOne(rasql.DB{}, ...) call.
				_, got := rasql.Returning(test.plan, projection)
				require.EqualError(t, got, test.err.Error())
			})
		}

	})

	// TestTypedMutationPlanValidation/"RETURNING is preflighted before the database is touched" proves a RETURNING
	// query against a dialect that cannot render RETURNING (MySQL) fails during
	// compilation, before the executor's Query or Exec is ever called: noCallHandle's
	// methods panic if reached, and none of them do. The canonical compiler
	// reports this as "RETURNING is not supported" rather than the old
	// "does not support RETURNING" wording, but the same thing is being proved:
	// the refusal happens at compile time, never at the database.
	t.Run("RETURNING is preflighted before the database is touched", func(t *testing.T) {
		first, _ := mutationValidationTables()
		name := query.TypedColumnOf[mutationValidationRow, string](first.Column("name"))
		id := query.TypedColumnOf[mutationValidationRow, int64](first.Column("id"))
		plan, err := rasql.NewCreatePlan(first, rasql.SetField(name, "x"))
		require.NoError(t, err)
		executor, err := rasql.Open(t.Context(), noCallHandle{}, dialect.MySQL(), rasql.WithProfile(rasql.MySQL84()))
		require.NoError(t, err)
		projection := mutationValidationProjection(t, first)

		createQuery, err := rasql.Returning(plan, projection)
		require.NoError(t, err)
		_, err = rasql.One(context.Background(), executor, createQuery)
		require.ErrorContains(t, err, "RETURNING is not supported")

		patch, err := rasql.NewPatchPlan(first, query.EqualValue(id, int64(1)), rasql.SetField(name, "x"))
		require.NoError(t, err)
		patchQuery, err := rasql.Returning(patch, projection)
		require.NoError(t, err)
		_, err = rasql.All(context.Background(), executor, patchQuery)
		require.ErrorContains(t, err, "RETURNING is not supported")
		require.False(t, errors.Is(err, sql.ErrNoRows))
		_, err = rasql.One(context.Background(), executor, patchQuery)
		require.ErrorContains(t, err, "RETURNING is not supported")
		require.False(t, errors.Is(err, sql.ErrNoRows))
	})
}

type noCallHandle struct{}

func (noCallHandle) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("QueryContext must not be called")
}

func (noCallHandle) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("ExecContext must not be called")
}

type writeUser struct {
	ID    int64
	Email string
}

type writeUserDecoder struct{ schema rasql.ResultSchema }

func (d writeUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (writeUserDecoder) Presence() []rasql.Presence         { return nil }
func (writeUserDecoder) DecodeRow(source rasql.ScanSource, result *writeUser) error {
	return source.Scan(&result.ID, &result.Email)
}

type writeFixture struct {
	table    rasql.Table[writeUser]
	executor rasql.Executor
	mock     sqlmock.Sqlmock
	id       rasql.Column[writeUser, int64]
	email    rasql.Column[writeUser, string]
}

func newWriteFixture(t *testing.T) writeFixture {
	t.Helper()
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	executor, err := rasql.Open(t.Context(), database, dialect.PostgreSQL(), rasql.WithProfile(rasql.PostgreSQL17()))
	require.NoError(t, err)
	table, err := rasql.TableOf[writeUser](schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	relation := table
	id, err := rasql.BindColumn[writeUser, int64](relation, "id", "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[writeUser, string](relation, "email", "")
	require.NoError(t, err)
	return writeFixture{table: table, executor: executor, mock: mock, id: id, email: email}
}

func writeProjection(t *testing.T, fixture writeFixture) rasql.Projection[writeUser] {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", fixture.id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", fixture.email.Expr(), schema.TextType{}, ""),
	}, writeUserDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func TestTypedWrite(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		fixture := newWriteFixture(t)
		plan, err := rasql.NewCreatePlan(fixture.table, rasql.SetField(fixture.id, int64(42)), rasql.SetField(fixture.email, "ada@example.com"))
		require.NoError(t, err)
		fixture.mock.ExpectExec(`INSERT INTO "users" \("id", "email"\) VALUES \(\$1, \$2\)`).
			WithArgs(int64(42), "ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))
		outcome, err := rasql.ExecMutation(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)
	})

	t.Run("update", func(t *testing.T) {
		fixture := newWriteFixture(t)
		plan, err := rasql.NewPatchPlan(fixture.table, rasql.EqualExpr(fixture.id.Expr(), rasql.Value(int64(42))), rasql.SetField(fixture.email, "grace@example.com"))
		require.NoError(t, err)
		fixture.mock.ExpectExec(`UPDATE "users" SET "email" = \$1 WHERE \("users"\."id" = \$2\)`).
			WithArgs("grace@example.com", int64(42)).WillReturnResult(sqlmock.NewResult(1, 1))
		outcome, err := rasql.ExecMutation(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)
	})

	t.Run("query write", func(t *testing.T) {
		fixture := newWriteFixture(t)
		projection := writeProjection(t, fixture)
		plan, err := rasql.NewCreatePlan(fixture.table, rasql.SetField(fixture.id, int64(42)), rasql.SetField(fixture.email, "ada@example.com"))
		require.NoError(t, err)
		returned, err := rasql.Returning(plan, projection)
		require.NoError(t, err)
		fixture.mock.ExpectQuery(`INSERT INTO`).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(42, "ada@example.com"))
		value, err := rasql.One(t.Context(), fixture.executor, returned)
		require.NoError(t, err)
		require.Equal(t, writeUser{ID: 42, Email: "ada@example.com"}, value)
	})
}

type generatedMeasurement struct {
	ID         int64
	Celsius    int64
	Fahrenheit int64
}

func generatedMeasurementTable(t *testing.T) (rasql.Table[generatedMeasurement], rasql.Executor, sqlmock.Sqlmock, func()) {
	t.Helper()
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	executor, err := rasql.Open(t.Context(), database, dialect.PostgreSQL(), rasql.WithProfile(rasql.PostgreSQL17()))
	require.NoError(t, err)
	table, err := rasql.TableOf[generatedMeasurement](schema.TableDef{Name: "measurements", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "celsius", Type: schema.IntegerType{}},
		{Name: "fahrenheit", Type: schema.IntegerType{}, GeneratedExpression: "celsius * 9 / 5 + 32", GeneratedStorage: schema.GeneratedStored},
	}})
	require.NoError(t, err)
	cleanup := func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	}
	return table, executor, mock, cleanup
}

func TestExecPointerWrite(t *testing.T) {
	t.Run("accepts pointer write statements", func(t *testing.T) {
		tests := []struct {
			name string
			run  func(*testing.T, rasql.Executor, *sql.DB, query.TableRef)
		}{
			{name: "insert", run: testPointerInsert},
			{name: "update", run: testPointerUpdate},
			{name: "delete", run: testPointerDelete},
			{name: "upsert", run: testPointerUpsert},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				database, err := sql.Open("sqlite", ":memory:")
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, database.Close()) })
				_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`)
				require.NoError(t, err)
				executor, err := rasql.Open(t.Context(), database, dialect.SQLite())
				require.NoError(t, err)
				testCase.run(t, executor, database, pointerWriteTable(t))
			})
		}
	})
}

// execPointerStatement adapts a validated query.WriteStatement, pointer or
// value, to the typed executor -- the point every subtest below proves: a
// pointer to a write statement is accepted exactly as the value is.
func execPointerStatement(t *testing.T, executor rasql.Executor, statement query.WriteStatement) {
	t.Helper()

	plan, err := rasql.NewStatementPlan(statement)
	require.NoError(t, err)
	_, err = rasql.ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
}

func testPointerInsert(t *testing.T, executor rasql.Executor, database *sql.DB, users query.TableRef) {
	id, email := users.Column("id"), users.Column("email")
	statement, err := query.NewInsert(users, query.Set(id, 1), query.Set(email, "ada@example.com"))
	require.NoError(t, err)
	execPointerStatement(t, executor, &statement)
	var stored string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT email FROM users WHERE id = 1`).Scan(&stored))
	require.Equal(t, "ada@example.com", stored)
}

func testPointerUpdate(t *testing.T, executor rasql.Executor, database *sql.DB, users query.TableRef) {
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (2, 'before@example.com')`)
	require.NoError(t, err)
	id, email := users.Column("id"), users.Column("email")
	statement, err := query.NewUpdate(users, query.Set(email, query.Bind("after@example.com")))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(2)))
	require.NoError(t, err)
	execPointerStatement(t, executor, &statement)
	var stored string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT email FROM users WHERE id = 2`).Scan(&stored))
	require.Equal(t, "after@example.com", stored)
}

func testPointerDelete(t *testing.T, executor rasql.Executor, database *sql.DB, users query.TableRef) {
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (3, 'delete@example.com')`)
	require.NoError(t, err)
	id := users.Column("id")
	statement, err := query.NewDelete(users)
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(3)))
	require.NoError(t, err)
	execPointerStatement(t, executor, &statement)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM users WHERE id = 3`).Scan(&count))
	require.Zero(t, count)
}

func testPointerUpsert(t *testing.T, executor rasql.Executor, database *sql.DB, users query.TableRef) {
	_, err := database.ExecContext(t.Context(), `INSERT INTO users (id, email) VALUES (4, 'before@example.com')`)
	require.NoError(t, err)
	id, email := users.Column("id"), users.Column("email")
	insert, err := query.NewInsert(users, query.Set(id, 4), query.Set(email, "after@example.com"))
	require.NoError(t, err)
	statement, err := query.NewUpsert(insert, []query.ColumnRef{id}, []query.Assignment{query.Set(email, query.Excluded(email))})
	require.NoError(t, err)
	execPointerStatement(t, executor, &statement)
	var stored string
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT email FROM users WHERE id = 4`).Scan(&stored))
	require.Equal(t, "after@example.com", stored)
}

func pointerWriteTable(t *testing.T) query.TableRef {
	t.Helper()
	users, err := query.NewTableRef(schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}

// tasksRow is the row type for the InSchema mutation fixtures below. id is
// the primary key, title is an ordinary required column, notes carries a
// default so a create plan can omit it, sequence is an identity-always
// column no field may target, and version is the optimistic-lock column.
type tasksRow struct {
	ID    int64
	Title string
}

func tasksDefinition() schema.TableDef {
	return schema.TableDef{
		Name:       "tasks",
		PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
			{Name: "notes", Type: schema.TextType{}, Default: "''"},
			{Name: "sequence", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "version", Type: schema.IntegerType{}},
		},
	}
}

func tasks(t *testing.T) rasql.Table[tasksRow] {
	t.Helper()
	table, err := rasql.TableOf[tasksRow](tasksDefinition())
	require.NoError(t, err)
	return table
}

// unrelatedTasksTable shares tasksRow's Go row type but names a different
// table, the same way mutationValidationTables' "second" table shares
// mutationValidationRow with "first": a column bound from it must still be
// refused by a plan built over tasks.
func unrelatedTasksTable(t *testing.T) rasql.Table[tasksRow] {
	t.Helper()
	table, err := rasql.TableOf[tasksRow](schema.TableDef{
		Name:       "labels",
		PrimaryKey: []string{"id"},
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	return table
}

// TestMutationPlanAcrossNamespace proves the fix for the defect where a
// mutation field bound from a table in its default namespace was refused by
// a plan targeting that same table moved with query.TableRef.InSchema: the
// two identity checks in typed_mutation.go used to compare by qualified name,
// which differs once the table is moved, even though the field and the plan
// still name the same table.
func TestMutationPlanAcrossNamespace(t *testing.T) {
	t.Run("create plan accepts a field bound from the default table and renders the moved namespace", func(t *testing.T) {
		home := tasks(t)
		id, err := rasql.BindColumn[tasksRow, int64](home, "id", "")
		require.NoError(t, err)
		title, err := rasql.BindColumn[tasksRow, string](home, "title", "")
		require.NoError(t, err)
		version, err := rasql.BindColumn[tasksRow, int64](home, "version", "")
		require.NoError(t, err)

		moved, err := home.InSchema("tenant_a")
		require.NoError(t, err)

		plan, err := rasql.NewCreatePlan(moved,
			rasql.SetField(id, int64(1)),
			rasql.SetField(title, "write the doc"),
			rasql.SetField(version, int64(1)))
		require.NoError(t, err)

		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		executor, err := rasql.Open(t.Context(), database, dialect.PostgreSQL(), rasql.WithProfile(rasql.PostgreSQL17()))
		require.NoError(t, err)
		mock.ExpectExec(`INSERT INTO "tenant_a"\."tasks" \("id", "title", "version"\) VALUES \(\$1, \$2, \$3\)`).
			WithArgs(int64(1), "write the doc", int64(1)).
			WillReturnResult(sqlmock.NewResult(1, 1))
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	})

	t.Run("patch plan accepts a field bound from the default table and renders the moved namespace", func(t *testing.T) {
		home := tasks(t)
		title, err := rasql.BindColumn[tasksRow, string](home, "title", "")
		require.NoError(t, err)

		moved, err := home.InSchema("tenant_a")
		require.NoError(t, err)
		movedID, err := rasql.BindColumn[tasksRow, int64](moved, "id", "")
		require.NoError(t, err)

		plan, err := rasql.NewPatchPlan(moved, rasql.EqualValue(movedID.Expr(), int64(7)), rasql.SetField(title, "renamed"))
		require.NoError(t, err)

		database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		executor, err := rasql.Open(t.Context(), database, dialect.PostgreSQL(), rasql.WithProfile(rasql.PostgreSQL17()))
		require.NoError(t, err)
		mock.ExpectExec(`UPDATE "tenant_a"\."tasks" SET "title" = \$1 WHERE \("tenant_a"\."tasks"\."id" = \$2\)`).
			WithArgs("renamed", int64(7)).
			WillReturnResult(sqlmock.NewResult(1, 1))
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	})

	t.Run("WithVersion accepts a version column bound from the default table against the moved table", func(t *testing.T) {
		home := tasks(t)
		version, err := rasql.BindColumn[tasksRow, int64](home, "version", "")
		require.NoError(t, err)
		title, err := rasql.BindColumn[tasksRow, string](home, "title", "")
		require.NoError(t, err)

		moved, err := home.InSchema("tenant_a")
		require.NoError(t, err)
		movedID, err := rasql.BindColumn[tasksRow, int64](moved, "id", "")
		require.NoError(t, err)

		patchPlan, err := rasql.NewPatchPlan(moved, rasql.EqualValue(movedID.Expr(), int64(7)), rasql.SetField(title, "renamed"))
		require.NoError(t, err)

		_, err = patchPlan.WithVersion(version, 3)
		require.NoError(t, err)
	})

	t.Run("WithVersion rejects a version column from an unrelated table", func(t *testing.T) {
		home := tasks(t)
		other := unrelatedTasksTable(t)
		title, err := rasql.BindColumn[tasksRow, string](home, "title", "")
		require.NoError(t, err)
		id, err := rasql.BindColumn[tasksRow, int64](home, "id", "")
		require.NoError(t, err)
		otherID, err := rasql.BindColumn[tasksRow, int64](other, "id", "")
		require.NoError(t, err)

		patchPlan, err := rasql.NewPatchPlan(home, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(title, "x"))
		require.NoError(t, err)

		_, err = patchPlan.WithVersion(otherID, 5)
		require.ErrorContains(t, err, "must belong to the patch table")
	})

	t.Run("WithVersion rejects a non-integer version column", func(t *testing.T) {
		home := tasks(t)
		notesAsVersion, err := rasql.BindColumn[tasksRow, int64](home, "notes", "")
		require.NoError(t, err)
		id, err := rasql.BindColumn[tasksRow, int64](home, "id", "")
		require.NoError(t, err)
		title, err := rasql.BindColumn[tasksRow, string](home, "title", "")
		require.NoError(t, err)

		patchPlan, err := rasql.NewPatchPlan(home, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(title, "x"))
		require.NoError(t, err)

		_, err = patchPlan.WithVersion(notesAsVersion, 5)
		require.ErrorContains(t, err, "must be a non-null ordinary integer")
	})

	t.Run("WithVersion rejects a nullable version column", func(t *testing.T) {
		// The version column is bound from a table where it is genuinely
		// non-null; the patch plan targets a second, independently built
		// table that merely shares its bare name and row type. The
		// loosened table-identity check accepts the pair, so this proves
		// the per-column checks that follow it -- read from the plan's own
		// table, not the column's source -- still catch a nullable column.
		source, err := rasql.TableOf[tasksRow](schema.TableDef{
			Name:       "tasks",
			PrimaryKey: []string{"id"},
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "version", Type: schema.IntegerType{}},
			},
		})
		require.NoError(t, err)
		version, err := rasql.BindColumn[tasksRow, int64](source, "version", "")
		require.NoError(t, err)

		nullableTarget, err := rasql.TableOf[tasksRow](schema.TableDef{
			Name:       "tasks",
			PrimaryKey: []string{"id"},
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "title", Type: schema.TextType{}},
				{Name: "version", Type: schema.IntegerType{}, Nullable: true},
			},
		})
		require.NoError(t, err)
		id, err := rasql.BindColumn[tasksRow, int64](nullableTarget, "id", "")
		require.NoError(t, err)
		title, err := rasql.BindColumn[tasksRow, string](nullableTarget, "title", "")
		require.NoError(t, err)

		patchPlan, err := rasql.NewPatchPlan(nullableTarget, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(title, "x"))
		require.NoError(t, err)

		_, err = patchPlan.WithVersion(version, 5)
		require.ErrorContains(t, err, "must be a non-null ordinary integer")
	})

	t.Run("WithVersion rejects a version column already assigned as a field", func(t *testing.T) {
		home := tasks(t)
		title, err := rasql.BindColumn[tasksRow, string](home, "title", "")
		require.NoError(t, err)
		version, err := rasql.BindColumn[tasksRow, int64](home, "version", "")
		require.NoError(t, err)
		id, err := rasql.BindColumn[tasksRow, int64](home, "id", "")
		require.NoError(t, err)

		patchPlan, err := rasql.NewPatchPlan(home, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(title, "x"), rasql.SetField(version, int64(2)))
		require.NoError(t, err)

		_, err = patchPlan.WithVersion(version, 5)
		require.ErrorContains(t, err, "is already assigned")
	})

	t.Run("a field targeting the always-identity column is rejected", func(t *testing.T) {
		home := tasks(t)
		sequence, err := rasql.BindColumn[tasksRow, int64](home, "sequence", "")
		require.NoError(t, err)

		_, err = rasql.NewCreatePlan(home, rasql.SetField(sequence, int64(99)))
		require.ErrorContains(t, err, "not writable")
	})

	t.Run("NULL into a non-nullable column is rejected", func(t *testing.T) {
		home := tasks(t)
		nullableTitle := query.NullableColumnOf[tasksRow, string](home.Column("title"))

		_, err := rasql.NewCreatePlan(home, rasql.ClearField(nullableTitle))
		require.ErrorContains(t, err, "does not accept NULL")
	})
}
