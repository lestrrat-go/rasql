package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

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

func TestGeneratedColumnMutationPlans(t *testing.T) {
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
}
