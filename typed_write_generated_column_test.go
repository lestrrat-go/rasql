package rasql_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestInsertOmitsGeneratedColumn proves that a generated column never
// reaches the default INSERT column list, because a database rejects a
// statement that targets one explicitly. Before this fix, typedInsertMany
// built its column list from every descriptor column except the ones an
// explicit rasql.DefaultColumns names, so a generated column reached the
// statement unless a caller remembered to exclude it by hand -- exactly the
// gap a rasqlgen-generated store for a table with a generated column would
// hit on its very first insert.
func TestInsertOmitsGeneratedColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type measurement struct {
		ID         int64 `rasql:"id"`
		Celsius    int64 `rasql:"celsius"`
		Fahrenheit int64 `rasql:"fahrenheit"`
	}
	measurements, err := rasql.TableOf[measurement](schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	// The expectation itself is the pin: it never mentions "fahrenheit". If
	// the generated column reached the statement, sqlmock would report an
	// unmatched query rather than this expectation firing.
	mock.ExpectExec(`INSERT INTO "measurements" ("id", "celsius") VALUES ($1, $2)`).
		WithArgs(int64(1), int64(20)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	_, err = rasql.Insert(t.Context(), db, measurements, measurement{ID: 1, Celsius: 20, Fahrenheit: 68})
	require.NoError(t, err)
}

// TestUpdateOmitsGeneratedColumn is the UPDATE counterpart to
// TestInsertOmitsGeneratedColumn: typedUpdateWithOptions built its default
// assignment list from every non-primary-key descriptor column, so a
// generated column reached a plain rasql.Update the same way it reached a
// plain rasql.Insert.
func TestUpdateOmitsGeneratedColumn(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	type measurement struct {
		ID         int64 `rasql:"id"`
		Celsius    int64 `rasql:"celsius"`
		Fahrenheit int64 `rasql:"fahrenheit"`
	}
	measurements, err := rasql.TableOf[measurement](schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	mock.ExpectExec(`UPDATE "measurements" SET "celsius" = $1 WHERE ("measurements"."id" = $2)`).
		WithArgs(int64(21), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = rasql.Update(t.Context(), db, measurements, measurement{ID: 1, Celsius: 21, Fahrenheit: 70})
	require.NoError(t, err)
}

// TestUpdateColumnsRejectsGeneratedColumn proves that a caller who explicitly
// names a generated column via rasql.UpdateColumns is refused up front, the
// same way naming a primary key is, rather than left to fail against the
// database once the statement reaches it.
func TestUpdateColumnsRejectsGeneratedColumn(t *testing.T) {
	type measurement struct {
		ID         int64 `rasql:"id"`
		Celsius    int64 `rasql:"celsius"`
		Fahrenheit int64 `rasql:"fahrenheit"`
	}
	measurements, err := rasql.TableOf[measurement](schema.TableDef{
		Name: "measurements",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "celsius", Type: schema.IntegerType{}},
			{
				Name:                "fahrenheit",
				Type:                schema.IntegerType{},
				GeneratedExpression: "celsius * 9 / 5 + 32",
				GeneratedStorage:    schema.GeneratedStored,
			},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)

	_, err = rasql.UpdateWithOptions(t.Context(), buildOnlyDB(t), measurements, measurement{}, rasql.UpdateColumns("fahrenheit"))
	require.ErrorContains(t, err, `column "fahrenheit" is generated and cannot be updated`)
}
