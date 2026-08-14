//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestInsertOmitsGeneratedColumnAgainstLiveDatabases proves, against real
// PostgreSQL and MySQL servers, the claim
// TestInsertOmitsGeneratedColumn/TestUpdateOmitsGeneratedColumn in
// typed_write_generated_column_test.go only ever prove against a mocked
// driver: that rasql.Insert into a table with a generated column succeeds,
// because typedInsertMany leaves the generated column out of the statement
// it builds, rather than being refused by the server the way it would be if
// the column reached the INSERT column list. #149 added that exclusion
// specifically to prevent a live INSERT from failing this way; until now
// nothing exercised it against a real engine.
//
// Each subtest inspects the table it just created with a raw CREATE TABLE,
// rather than hand-building a schema.TableDef, so the descriptor Insert
// builds its statement from is the same one this PR's inspection changes
// actually produce -- proving the inspect and typed-write halves of
// generated-column support work together, not merely each in isolation.
func TestInsertOmitsGeneratedColumnAgainstLiveDatabases(t *testing.T) {
	for _, test := range []struct {
		name                  string
		open                  func(*testing.T) *sql.DB
		dialect               dialect.Dialect
		createTableQuery      string
		selectFahrenheitQuery string
	}{
		{
			name:    "postgresql",
			open:    dbtest.PostgreSQLDB,
			dialect: dialect.PostgreSQL(),
			createTableQuery: "CREATE TABLE live_write_measurements (" +
				"id BIGINT PRIMARY KEY, " +
				"celsius BIGINT NOT NULL, " +
				"fahrenheit BIGINT GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED" +
				")",
			selectFahrenheitQuery: "SELECT fahrenheit FROM live_write_measurements WHERE id = $1",
		},
		{
			name:    "mysql",
			open:    dbtest.MySQLDB,
			dialect: dialect.MySQL(),
			createTableQuery: "CREATE TABLE live_write_measurements (" +
				"id BIGINT PRIMARY KEY, " +
				"celsius BIGINT NOT NULL, " +
				"fahrenheit BIGINT GENERATED ALWAYS AS (celsius * 9 / 5 + 32) STORED" +
				") ENGINE=InnoDB",
			selectFahrenheitQuery: "SELECT fahrenheit FROM live_write_measurements WHERE id = ?",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database := test.open(t)

			// render.CreateTable refuses to build DDL for a generated
			// column (see UnsupportedGeneratedColumnError), so the table
			// is created directly rather than through rasql.CreateTable.
			_, err := database.ExecContext(ctx, test.createTableQuery)
			require.NoError(t, err, "create live table with a generated column")

			inspector, err := inspect.New(database, test.dialect)
			require.NoError(t, err, "create inspector")
			definition, err := inspector.Table(ctx, "live_write_measurements")
			require.NoError(t, err, "inspect the table this subtest just created")

			db, err := rasql.New(database, test.dialect)
			require.NoError(t, err, "create rasql db")
			type measurement struct {
				ID         int64 `rasql:"id"`
				Celsius    int64 `rasql:"celsius"`
				Fahrenheit int64 `rasql:"fahrenheit"`
			}
			measurements, err := rasql.TableOf[measurement](definition)
			require.NoError(t, err, "build a typed table from the inspected descriptor")

			// The point under test: this must succeed. If typedInsertMany
			// ever again named the generated column explicitly, the server
			// itself -- not a mock -- would refuse the statement here.
			_, err = rasql.Insert(ctx, db, measurements, measurement{ID: 1, Celsius: 20, Fahrenheit: 999})
			require.NoError(t, err, "insert into a table with a generated column must succeed: the generated column must not reach the INSERT statement")

			// Read the row back directly, bypassing the typed read path,
			// to confirm a row genuinely landed and that the server, not
			// the value this test supplied, computed the generated column:
			// the 999 above must not have reached the database at all.
			var storedFahrenheit int64
			err = database.QueryRowContext(ctx, test.selectFahrenheitQuery, int64(1)).Scan(&storedFahrenheit)
			require.NoError(t, err, "read the inserted row back directly")
			require.Equal(t, int64(68), storedFahrenheit, "the server, not the caller, must have computed the generated column")
		})
	}
}
