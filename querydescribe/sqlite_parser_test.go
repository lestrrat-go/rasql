package querydescribe

import "testing"

func TestParseCountProjectionMatrix(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"star", `COUNT(*) AS n`, "n"},
		{"identifier", `count(id) AS n`, "n"},
		{"qualified", `COUNT(t.id) AS n`, "n"},
		{"double quoted", `COUNT("t"."id") AS "n"`, "n"},
		{"backtick quoted", "COUNT(`t`.`id`) AS `n`", "n"},
		{"bracket quoted", `COUNT([t].[id]) AS [n]`, "n"},
		{"comments", "/*a*/ count /*b*/ ( /*c*/ t /*d*/ . /*e*/ id /*f*/ ) /*g*/ as /*h*/ n", "n"},
		{"line comment eof", "COUNT(*) AS n -- complete", "n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseCountProjection(test.sql); got != test.want {
				t.Fatalf("parseCountProjection(%q) = %q, want %q", test.sql, got, test.want)
			}
		})
	}
}

func TestParseCountProjectionRejectsOtherExpressions(t *testing.T) {
	tests := []string{
		`COUNT(DISTINCT id) AS n`, `COUNT(id) FILTER (WHERE id > 0) AS n`, `COUNT(id) OVER () AS n`,
		`COUNT(id) n`, `COUNT(id)+1 AS n`, `SUM(id) AS n`, `CAST(COUNT(*) AS TEXT) AS n`,
		`CASE WHEN id > 0 THEN 1 END AS n`, `AVG(id) AS n`, `MIN(id) AS n`, `MAX(id) AS n`,
		`TOTAL(id) AS n`, `GROUP_CONCAT(id) AS n`, `json_extract(id, '$.x') AS n`, `user_function(id) AS n`,
		`'COUNT(*) AS n'`, `COUNT(*) AS [n] trailing`, `COUNT(*) AS`,
		`COUNT("unterminated) AS n`, `COUNT(/* unterminated) AS n`,
	}
	for _, expression := range tests {
		t.Run(expression, func(t *testing.T) {
			if got := parseCountProjection(expression); got != "" {
				t.Fatalf("parseCountProjection(%q) = %q, want rejection", expression, got)
			}
		})
	}
}

func TestCountProjectionRequiresExpectedPositionAndAlias(t *testing.T) {
	sqlText := `SELECT id, COUNT(*) AS n FROM t`
	if countProjection(sqlText, "n", 0, 2) {
		t.Fatal("countProjection accepted COUNT at the wrong projection index")
	}
	if !countProjection(sqlText, "n", 1, 2) {
		t.Fatal("countProjection rejected COUNT at the expected projection index")
	}
	if countProjection(`SELECT COUNT(*) AS N FROM t`, "n", 0, 1) {
		t.Fatal("countProjection ignored case in the alias")
	}
}

func TestValidCountQueryShapeRejectsUnsupportedStatements(t *testing.T) {
	tests := []string{
		`SELECT COUNT(*) AS n FROM t UNION SELECT COUNT(*) AS n FROM t`,
		`SELECT COUNT(*) AS n FROM t INTERSECT SELECT COUNT(*) AS n FROM t`,
		`SELECT COUNT(*) AS n FROM t EXCEPT SELECT COUNT(*) AS n FROM t`,
		`VALUES (1)`, `WITH x AS (SELECT 1) SELECT COUNT(*) AS n FROM x`,
		`SELECT (SELECT COUNT(*) FROM t) AS n FROM t`, `SELECT COUNT(*) AS n FROM t; SELECT 1`,
		`SELECT COUNT(*) AS n FROM t )`, `SELECT COUNT(*) AS n FROM t /* unterminated`,
		`SELECT COUNT(*) AS n FROM t 'unterminated`, `SELECT COUNT(*) AS n`,
	}
	for _, sqlText := range tests {
		t.Run(sqlText, func(t *testing.T) {
			if validCountQueryShape(sqlText) {
				t.Fatalf("validCountQueryShape(%q) accepted unsupported query", sqlText)
			}
		})
	}
}

func TestOneStatementAllowsSemicolonsInLiteralsAndComments(t *testing.T) {
	for _, sqlText := range []string{
		`SELECT 'one;two' AS value`, `SELECT "semi;colon" AS value`,
		"SELECT 1 -- semi;colon\n", "SELECT 1 /* semi;colon */",
	} {
		if err := oneStatement(sqlText); err != nil {
			t.Errorf("oneStatement(%q) returned %v", sqlText, err)
		}
	}
}
