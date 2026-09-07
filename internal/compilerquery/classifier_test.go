package compilerquery

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/namedsql"
)

func TestClassifySQLRejectsUnsafeStatements(t *testing.T) {
	for _, sqlText := range []string{"WITH x AS (SELECT 1) SELECT 1", "CREATE TABLE x (id int)", "SELECT 1; SELECT 2", "CALL x()", "PRAGMA foreign_keys"} {
		if _, err := ClassifySQL(sqlText); err == nil {
			t.Errorf("ClassifySQL(%q) succeeded", sqlText)
		}
	}
}

func TestAnalyzerUsesNamedSQLRepeatedPositions(t *testing.T) {
	template, err := namedsql.Parse("q", `SELECT * FROM t WHERE a = {{bind "id" t.a}} OR b = {{bind "id" t.a}}`)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := template.Compile(dialect.PostgreSQL())
	if err != nil {
		t.Fatal(err)
	}
	if compiled.SQL() != `SELECT * FROM t WHERE a = $1 OR b = $2` {
		t.Fatalf("SQL=%q", compiled.SQL())
	}
	if got := compiled.QueryDef().Parameters; len(got) != 2 || got[0] != "id" || got[1] != "id" {
		t.Fatalf("names=%#v", got)
	}
}
