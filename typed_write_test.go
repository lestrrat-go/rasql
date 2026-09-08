package rasql_test

import (
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type writeUser struct {
	ID    int64
	Email string
}

type generatedReturningUser struct {
	ID    int64  `rasql:"-"`
	Email string `rasql:"-"`
}

func (u *generatedReturningUser) ScanColumns() []string { return []string{"id", "email"} }
func (u *generatedReturningUser) ScanRow(source rasql.ScanSource) error {
	return source.Scan(&u.ID, &u.Email)
}
func (u *generatedReturningUser) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	for index, column := range columns {
		switch column {
		case "id":
			destinations[index] = &u.ID
		case "email":
			destinations[index] = &u.Email
		default:
			return nil, fmt.Errorf("unknown column %q", column)
		}
	}
	return destinations, nil
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

func TestInsert(t *testing.T) {
	fixture := newWriteFixture(t)
	plan, err := rasql.NewCreatePlan(fixture.table, rasql.SetField(fixture.id, int64(42)), rasql.SetField(fixture.email, "ada@example.com"))
	require.NoError(t, err)
	fixture.mock.ExpectExec(`INSERT INTO "users" \("id", "email"\) VALUES \(\$1, \$2\)`).
		WithArgs(int64(42), "ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))
	outcome, err := rasql.ExecMutation(t.Context(), fixture.executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
}

func TestQueryWrite(t *testing.T) {
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
}

func TestUpdate(t *testing.T) {
	fixture := newWriteFixture(t)
	plan, err := rasql.NewPatchPlan(fixture.table, rasql.EqualExpr(fixture.id.Expr(), rasql.Value(int64(42))), rasql.SetField(fixture.email, "grace@example.com"))
	require.NoError(t, err)
	fixture.mock.ExpectExec(`UPDATE "users" SET "email" = \$1 WHERE \("users"\."id" = \$2\)`).
		WithArgs("grace@example.com", int64(42)).WillReturnResult(sqlmock.NewResult(1, 1))
	outcome, err := rasql.ExecMutation(t.Context(), fixture.executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
}
