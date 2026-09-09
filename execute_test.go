package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type executionRow struct{ ID int64 }

type executionDecoder struct{ schema rasql.ResultSchema }

func (d executionDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (executionDecoder) Presence() []rasql.Presence         { return nil }
func (executionDecoder) DecodeRow(source rasql.ScanSource, result *executionRow) error {
	return source.Scan(&result.ID)
}

func executionProjection(t *testing.T) rasql.Projection[executionRow] {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NativeProjection(executionDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func TestExecution(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
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

	query, err := rasql.Native(rasql.NativeStatement{Engine: "postgresql", SQL: "SELECT id FROM users WHERE id = $1", Args: []rasql.NativeArgument{{Value: int64(42)}}}, executionProjection(t), rasql.Many)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users WHERE id = $1").WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
	rows, err := rasql.All(t.Context(), executor, query)
	require.NoError(t, err)
	require.Equal(t, []executionRow{{ID: 42}}, rows)

	type user struct {
		ID    int64
		Email string
	}
	table, err := rasql.TableOf[user](schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[user, string](relation, "email", "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[user, int64](relation, "id", "")
	require.NoError(t, err)
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(email, "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").WithArgs(int64(1), "ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))
	outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
}
