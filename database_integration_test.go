package rasql_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestDatabaseIntegration(t *testing.T) {
	for _, test := range []struct {
		name       string
		driverName string
		dsn        string
		dialect    dialect.Dialect
	}{
		{
			name:       "postgresql",
			driverName: "pgx",
			dsn:        os.Getenv("RASQL_TEST_POSTGRES_DSN"),
			dialect:    dialect.PostgreSQL(),
		},
		{
			name:       "mysql",
			driverName: "mysql",
			dsn:        os.Getenv("RASQL_TEST_MYSQL_DSN"),
			dialect:    dialect.MySQL(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.dsn == "" {
				t.Skipf("set %s to run this integration test", dsnEnvironmentName(test.name))
			}
			testDatabaseIntegration(t, test.driverName, test.dsn, test.dialect)
		})
	}
}

func dsnEnvironmentName(databaseName string) string {
	switch databaseName {
	case "postgresql":
		return "RASQL_TEST_POSTGRES_DSN"
	case "mysql":
		return "RASQL_TEST_MYSQL_DSN"
	default:
		return "database DSN environment variable"
	}
}

func testDatabaseIntegration(t *testing.T, driverName string, dsn string, d dialect.Dialect) {
	database, err := sql.Open(driverName, dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	require.NoError(t, database.PingContext(t.Context()))

	client, err := rasql.New(database, d)
	require.NoError(t, err)
	type record struct {
		ID     int64  `rasql:"id"`
		Active bool   `rasql:"active"`
		Email  string `rasql:"email"`
	}
	records, err := rasql.NewTable[record](integrationTable())
	require.NoError(t, err)
	recordID, err := records.Column("id")
	require.NoError(t, err)

	_, err = database.ExecContext(t.Context(), "DROP TABLE IF EXISTS rasql_integration_records")
	require.NoError(t, err)
	defer func() {
		_, err := database.ExecContext(t.Context(), "DROP TABLE IF EXISTS rasql_integration_records")
		require.NoError(t, err)
	}()
	require.NoError(t, rasql.Create(t.Context(), client, records))

	first := record{ID: 1, Active: true, Email: "ada@example.com"}
	second := record{ID: 2, Active: false, Email: "grace@example.com"}
	_, err = rasql.Insert(t.Context(), client, records, first)
	require.NoError(t, err)
	_, err = rasql.Insert(t.Context(), client, records, second)
	require.NoError(t, err)

	first.Email = "ada.lovelace@example.com"
	_, err = rasql.Update(t.Context(), client, records, first)
	require.NoError(t, err)

	actual, err := rasql.SelectFrom(client, records).WhereEqual(recordID, first.ID).One(t.Context())
	require.NoError(t, err)
	require.Equal(t, first, actual)

	all, err := rasql.SelectFrom(client, records).OrderAsc(recordID).All(t.Context())
	require.NoError(t, err)
	require.Equal(t, []record{first, second}, all)

	inspector, err := inspect.New(database, d)
	require.NoError(t, err)
	inspected, err := inspector.Table(t.Context(), "rasql_integration_records")
	require.NoError(t, err)
	require.Equal(t, integrationTable(), inspected)
}

func integrationTable() schema.Table {
	return schema.Table{
		Name: "rasql_integration_records",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "active", Type: schema.TypeBoolean},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
}
