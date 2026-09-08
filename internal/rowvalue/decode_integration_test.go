//go:build unix

package rowvalue_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/stretchr/testify/require"
)

// TestAggregatesOverIntegerColumnDecodeAgainstLiveDatabases pins the engine
// fact the text-encoded numeric path exists for. MySQL computes both SUM() and
// AVG() over an INTEGER column as DECIMAL, and its driver delivers a DECIMAL
// as bytes, so the value reaching rasql is []byte("350") and not int64(350).
// PostgreSQL computes the sum as bigint and delivers int64, and computes the
// average as NUMERIC, which pgx delivers as a string.
//
// Every other test of this path builds the driver value by hand, which cannot
// confirm what either server actually sends. This one reads the value off a
// live connection first, asserts its Go type, and only then decodes it, so a
// server that changes what it sends fails here rather than silently widening
// what the decoder has to accept.
func TestAggregatesOverIntegerColumnDecodeAgainstLiveDatabases(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(*testing.T) *sql.DB
		// sum and average are the Go types the engine's driver hands back.
		// The engines disagree, which is the whole point of the test.
		sum     reflect.Type
		average reflect.Type
	}{
		{
			name:    "postgresql",
			open:    dbtest.PostgreSQLDB,
			sum:     reflect.TypeFor[int64](),
			average: reflect.TypeFor[string](),
		},
		{
			name:    "mysql",
			open:    dbtest.MySQLDB,
			sum:     reflect.TypeFor[[]byte](),
			average: reflect.TypeFor[[]byte](),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			database := test.open(t)

			_, err := database.ExecContext(ctx, "CREATE TABLE totals (amount INTEGER NOT NULL)")
			require.NoError(t, err, "create table")
			_, err = database.ExecContext(ctx, "INSERT INTO totals (amount) VALUES (100), (250)")
			require.NoError(t, err, "seed rows")

			var deliveredSum, deliveredAverage any
			require.NoError(t,
				database.QueryRowContext(ctx, "SELECT SUM(amount), AVG(amount) FROM totals").
					Scan(&deliveredSum, &deliveredAverage),
				"read the aggregates",
			)
			require.Equal(t, test.sum, reflect.TypeOf(deliveredSum),
				"the driver value the engine delivers for SUM() over an integer column",
			)
			require.Equal(t, test.average, reflect.TypeOf(deliveredAverage),
				"the driver value the engine delivers for AVG() over an integer column",
			)

			row, err := rowvalue.NewRow(
				[]string{"total", "average"},
				[]any{deliveredSum, deliveredAverage},
			)
			require.NoError(t, err)

			var total int64
			require.NoError(t, rowvalue.Assign(row, "total", &total), "decode the sum")
			require.Equal(t, int64(350), total)

			var average float64
			require.NoError(t, rowvalue.Assign(row, "average", &average), "decode the average")
			require.InDelta(t, 175.0, average, 0)
		})
	}
}
