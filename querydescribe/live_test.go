package querydescribe_test

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/querydescribe"
)

func TestPostgreSQLDescribePrepareOnlyLive(t *testing.T) {
	dsn := os.Getenv("RASQL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("RASQL_TEST_POSTGRES_DSN is not set")
	}
	description, err := querydescribe.NewPostgreSQL().Describe(t.Context(), compilerquery.DescribeRequest{DSN: dsn, Name: "g4_live", SQL: "SELECT $1::int4 AS value", ParameterNames: []string{"value"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Parameters) != 1 || len(description.Results) != 1 {
		t.Fatalf("description=%#v", description)
	}
}

func TestMySQLDescribePrepareOnlyLive(t *testing.T) {
	dsn := os.Getenv("RASQL_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("RASQL_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := querydescribe.NewMySQL(db).Describe(t.Context(), compilerquery.DescribeRequest{DB: db, SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
}
