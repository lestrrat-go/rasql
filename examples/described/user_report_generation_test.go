package described

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/querydescribe"
	_ "modernc.org/sqlite"
)

func TestUserReportOwnerMatchesSQLiteGeneration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE users(id INTEGER); CREATE TABLE profiles(user_id INTEGER, nickname TEXT)"); err != nil {
		t.Fatal(err)
	}
	queryBytes, err := os.ReadFile(filepath.Join("user_report.sql"))
	if err != nil {
		t.Fatal(err)
	}
	query := string(queryBytes)
	description, err := querydescribe.NewSQLite(db).Describe(context.Background(), querydescribe.Request{
		Name:        "UserReport",
		SQL:         query,
		Cardinality: querydescribe.Many,
	})
	if err != nil {
		t.Fatal(err)
	}
	generated, err := querygen.GoSource(namedsql.QueryDef{Name: "UserReport", SQL: query, Result: &description}, "described", "UserReport")
	if err != nil {
		t.Fatal(err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("user_report_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(generated) != string(checkedIn) {
		t.Fatal("checked-in generated owner is stale; regenerate user_report_gen.go from user_report.sql")
	}
}
