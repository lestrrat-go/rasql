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

type usersRowDecoder struct{ schema rasql.ResultSchema }

func (d usersRowDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (usersRowDecoder) Presence() []rasql.Presence         { return nil }
func (d usersRowDecoder) DecodeRow(source rasql.ScanSource, row *store.UsersRow) error {
	return source.Scan(&row.ID, &row.Email, &row.Nickname, &row.Status, &row.FirstName, &row.LastName)
}

// usersRowProjection reads store.UsersRow back through the same table
// store.Users() names, binding each of its own columns rather than a second,
// hand-declared one -- DynamicProjection does not fit here: it decodes an
// existing result set, but a RETURNING projection needs a real expression
// for every column it names, and DynamicProjection's items are that column's
// name projecting a bound NULL, not the column itself.
func usersRowProjection(t *testing.T) rasql.Projection[store.UsersRow] {
	t.Helper()

	relation, err := rasql.SourceOf[store.UsersRow](store.Users(), "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[store.UsersRow, int64](relation, "id", "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[store.UsersRow, string](relation, "email", "")
	require.NoError(t, err)
	nickname, err := rasql.BindNullColumn[store.UsersRow, string](relation, "nickname", "")
	require.NoError(t, err)
	status, err := rasql.BindColumn[store.UsersRow, string](relation, "status", "")
	require.NoError(t, err)
	firstName, err := rasql.BindColumn[store.UsersRow, string](relation, "first_name", "")
	require.NoError(t, err)
	lastName, err := rasql.BindColumn[store.UsersRow, string](relation, "last_name", "")
	require.NoError(t, err)

	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
		rasql.ResultColumn{Name: "status", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "first_name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "last_name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", email.Expr(), schema.TextType{}, ""),
		rasql.NullItem("nickname", nickname.NullExpr(), schema.TextType{}, ""),
		rasql.Item("status", status.Expr(), schema.TextType{}, ""),
		rasql.Item("first_name", firstName.Expr(), schema.TextType{}, ""),
		rasql.Item("last_name", lastName.Expr(), schema.TextType{}, ""),
	}, usersRowDecoder{schema: resultSchema})
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
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		projection := usersRowProjection(t)

		createQuery, err := rasql.Returning(store.NewUsersCreate().Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan(), projection)
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
		require.Nil(t, updated.Nickname)
	})

	t.Run("generated-column plans", func(t *testing.T) {
		table, executor, mock, cleanup := generatedMeasurementTable(t)
		t.Cleanup(cleanup)
		relation, err := rasql.SourceOf(table, "")
		require.NoError(t, err)
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

func queryEqualID(id int64) query.Predicate {
	return query.EqualValue(store.Users().ID(), id)
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

	relation, err := rasql.SourceOf(first, "")
	require.NoError(t, err)
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
		// is handing that valid query an invalid database -- rasql.DB{} in place
		// of the old zero rasql.DB.
		invalidProfile, err := rasql.EngineProfileFromVersion("mysql-8.4", 8, 4, 0)
		require.NoError(t, err)
		_, terminalErr = rasql.AsExecutor(rasql.DB{}, invalidProfile)
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
		db, err := rasql.New(noCallHandle{}, dialect.MySQL())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("mysql-8.4", 8, 4, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
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
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := rasql.TableOf[writeUser](schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
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
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
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
				db, err := rasql.New(database, dialect.SQLite())
				require.NoError(t, err)
				profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
				require.NoError(t, err)
				executor, err := rasql.AsExecutor(db, profile)
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
