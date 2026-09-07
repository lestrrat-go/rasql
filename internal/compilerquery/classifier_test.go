package compilerquery

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
)

func TestClassifySQLRejectsUnsafeStatements(t *testing.T) {
	for _, sqlText := range []string{"WITH x AS (SELECT 1) SELECT 1", "CREATE TABLE x (id int)", "SELECT 1; SELECT 2", "CALL x()", "PRAGMA foreign_keys"} {
		if _, err := ClassifySQL(sqlText); err == nil {
			t.Errorf("ClassifySQL(%q) succeeded", sqlText)
		}
	}
}

func TestLowerNamedSQLPreservesRepeatedPositions(t *testing.T) {
	sqlText, names, err := lowerNamedSQL(`SELECT * FROM t WHERE a = {{bind "id" t.a}} OR b = {{bind "id" t.a}}`, dialect.PostgreSQL())
	if err != nil {
		t.Fatal(err)
	}
	if sqlText != `SELECT * FROM t WHERE a = $1 OR b = $2` {
		t.Fatalf("SQL=%q", sqlText)
	}
	if len(names) != 2 || names[0] != "id" || names[1] != "id" {
		t.Fatalf("names=%#v", names)
	}
}

func TestLowerNamedSQLRejectsConflictingColumnReferences(t *testing.T) {
	_, _, err := lowerNamedSQL(`SELECT * FROM t WHERE a = {{bind "id" t.a}} OR b = {{bind "id" t.b}}`, dialect.PostgreSQL())
	if err == nil {
		t.Fatal("expected conflicting reference error")
	}
}
