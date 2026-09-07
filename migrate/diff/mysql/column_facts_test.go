package mysql

import (
	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/sqltext"
	"reflect"
	"strings"
	"testing"
)

// scanColumnFact adapts a column fragment to the sole production token grammar.
func scanColumnFact(rest string) (columnFacts, [][2]int, error) {
	source := "value" + rest
	tokens, err := lexMySQLSource(source)
	if err != nil {
		return columnFacts{}, nil, err
	}
	fact, ranges, err := scanColumnFactTokens(source, tokens)
	for i := range ranges {
		ranges[i][0]--
		ranges[i][1]--
	}
	return fact, ranges, err
}

func TestScanColumnFactsIgnoresQuotedCommentsAndNestedText(t *testing.T) {
	fact, _, err := scanColumnFact(" DEFAULT 'COLLATE utf8mb4_bin GENERATED AS (x)' /* AUTO_INCREMENT */ COLLATE `utf8mb4_bin`")
	if err != nil || fact.Collation == nil || fact.AutoIncrement {
		t.Fatalf("unexpected facts: %#v, %v", fact, err)
	}
}

func TestScanColumnFactsGeneratedForms(t *testing.T) {
	for _, tc := range []struct{ source, storage string }{
		{" GENERATED ALWAYS AS (json_extract(payload, '$.x')) STORED", "STORED"},
		{" GENERATED AS (a + (b * 2))", "VIRTUAL"},
	} {
		fact, _, err := scanColumnFact(tc.source)
		if err != nil || fact.Generated == nil || fact.Generated.Storage != tc.storage {
			t.Fatalf("source %q: %#v, %v", tc.source, fact, err)
		}
	}
}

func TestScanColumnFactsRejectsGeneratedCombinations(t *testing.T) {
	for _, source := range []string{" GENERATED AS (x) DEFAULT 1", " GENERATED AS (x) AUTO_INCREMENT"} {
		if _, _, err := scanColumnFact(source); err == nil {
			t.Fatalf("source %q was accepted", source)
		}
	}
}

func TestScanColumnFactsRejectsUnbalancedGeneratedExpression(t *testing.T) {
	if _, _, err := scanColumnFact(" GENERATED AS (a + (b)"); err == nil {
		t.Fatal("unbalanced expression was accepted")
	}
}

func TestScanColumnFactsRejectsDuplicateAttributes(t *testing.T) {
	for _, source := range []string{" COLLATE utf8mb4_bin COLLATE latin1_bin", " AUTO_INCREMENT AUTO_INCREMENT", " GENERATED AS (x) GENERATED AS (y)"} {
		if _, _, err := scanColumnFact(source); err == nil {
			t.Fatalf("accepted duplicate attribute: %s", source)
		}
	}
}

func TestCloneColumnFactsPreservesNestedMetadata(t *testing.T) {
	original, _, err := scanColumnFact(" COLLATE `utf8mb4_bin` GENERATED ALWAYS AS (payload + 1) STORED AUTO_INCREMENT")
	if err == nil {
		t.Fatal("expected incompatible generated and auto increment facts")
	}
	original, _, err = scanColumnFact(" COLLATE `utf8mb4_bin` GENERATED ALWAYS AS (payload + 1) STORED")
	if err != nil {
		t.Fatal(err)
	}
	copy := cloneColumnFacts(map[string]columnFacts{"value": original})
	if !reflect.DeepEqual(copy["value"], original) {
		t.Fatalf("clone changed sidecar facts: %#v %#v", original, copy["value"])
	}
	copy["value"].Generated.Expression = "changed"
	if original.Generated.Expression == "changed" {
		t.Fatal("clone aliases generated metadata")
	}
}

func TestParseValidatesAutoIncrementKeyAndType(t *testing.T) {
	a := New()
	if _, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE t (id bigint AUTO_INCREMENT PRIMARY KEY);")}}); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"CREATE TABLE t (id varchar(20) AUTO_INCREMENT PRIMARY KEY);",
		"CREATE TABLE t (id bigint AUTO_INCREMENT);",
	} {
		if _, err := a.Parse([]diff.Source{{SQL: sqltext.Text(source)}}); err == nil {
			t.Fatalf("accepted invalid auto increment schema: %s", source)
		}
	}
	if _, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE t (a bigint AUTO_INCREMENT PRIMARY KEY, b bigint AUTO_INCREMENT UNIQUE);")}}); err == nil {
		t.Fatal("accepted multiple auto increment columns")
	}
}

func TestParseRoutesQuotedAndCommentedTableColumns(t *testing.T) {
	a := New()
	_, err := a.Parse([]diff.Source{{SQL: sqltext.Text("CREATE TABLE IF NOT EXISTS `weird``name` (/* comma, COLLATE fake */ `id``x` bigint AUTO_INCREMENT PRIMARY KEY, `label` varchar(20) DEFAULT 'GENERATED AS (x),'); CREATE TABLE `second` (`value` varchar(10) COLLATE `utf8mb4_bin`);")}})
	if err != nil {
		t.Fatal(err)
	}
	masked, tables, err := stripColumnFacts("CREATE TABLE `second` (`value` varchar(10) COLLATE `utf8mb4_bin`);")
	if err != nil || !strings.Contains(masked, "value") || len(tables) != 1 || tables[0].Columns["value"].Collation == nil {
		t.Fatalf("boundary facts lost: %q %#v %v", masked, tables, err)
	}
}

func TestParsePublicMisleadingAndDecodedFacts(t *testing.T) {
	source := `/* CREATE TABLE ignored (x int, y int); */
-- CREATE TABLE ignored_two (z int);
CREATE TABLE IF NOT EXISTS ` + "`odd``table`" + ` (
  ` + "`id``part`" + ` bigint AUTO_INCREMENT,
  ` + "`label`" + ` varchar(64) COLLATE ` + "`utf8mb4``bin`" + ` NOT NULL DEFAULT 'CREATE TABLE fake(a,b)',
  ` + "`note`" + ` varchar(64) DEFAULT 'comma, paren ) and CREATE TABLE fake_two(x), escaped \'quote',
  CONSTRAINT ` + "`odd_pk`" + ` PRIMARY KEY (` + "`id``part`, `label`" + `)
);`
	snapshot, err := New().Parse([]diff.Source{{Path: "odd.sql", SQL: sqltext.Text(source)}})
	if err != nil {
		t.Fatal(err)
	}
	tables := snapshot.(*schemaSnapshot).tables
	table, ok := tables[tableNameKey(mysqlquery.QualifiedName{{Name: "odd`table"}}, LowerCaseTableNamesCaseSensitive)]
	if !ok {
		t.Fatalf("decoded table key missing: %#v", tables)
	}
	if table.statement == nil || !table.columns["id`part"].AutoIncrement {
		t.Fatalf("decoded table or auto increment fact missing: %#v", table)
	}
	if _, ok := table.columns["label"]; !ok {
		t.Fatalf("decoded label column missing: %#v", table.statement.Columns)
	}
	if table.columns["label"].Collation == nil || (*table.columns["label"].Collation)[0].Name != "utf8mb4`bin" {
		t.Fatalf("decoded collation missing: %#v", table.columns["label"])
	}
	if len(table.statement.Columns) != 3 {
		t.Fatalf("decoded note column missing: %#v", table.statement.Columns)
	}
	for index, want := range []string{"id`part", "label", "note"} {
		if got := table.statement.Columns[index].Name.Name; got != want {
			t.Fatalf("column %d name = %q, want %q", index, got, want)
		}
	}
	label := table.statement.Columns[1]
	rendered, err := renderFullColumn(columnDefinition{AST: label, Facts: table.columns["label"]})
	if err != nil {
		t.Fatal(err)
	}
	want := "`label` varchar(64) COLLATE `utf8mb4``bin` NOT NULL DEFAULT 'CREATE TABLE fake(a,b)'"
	if rendered != want {
		t.Fatalf("rendered label = %q, want %q", rendered, want)
	}
}

func TestParsePublicFactsCarrierAndRender(t *testing.T) {
	source := "CREATE TABLE facts (" +
		"`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, " +
		"`label` VARCHAR(64) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'new', " +
		"`total` DECIMAL(12,2) GENERATED ALWAYS AS ((`quantity` * (`price` + 1))) STORED, " +
		"`virtual_total` DECIMAL(12,2) GENERATED AS (`quantity` + (`price` * 2))" +
		");"
	_, _, scanErr := stripColumnFacts(source)
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	snapshot, err := New().Parse([]diff.Source{{Path: "facts.sql", SQL: sqltext.Text(source)}})
	if err != nil {
		t.Fatal(err)
	}
	table := snapshot.(*schemaSnapshot).tables[tableNameKey(mysqlquery.QualifiedName{{Name: "facts"}}, LowerCaseTableNamesCaseSensitive)]
	if table.statement == nil {
		t.Fatal("facts table statement missing")
	}
	if len(table.columns) != 4 {
		t.Fatalf("facts carrier count = %d, want 4: %#v", len(table.columns), table.columns)
	}
	if !table.columns["id"].AutoIncrement || table.columns["id"].Generated != nil || table.columns["id"].Collation != nil {
		t.Fatalf("id facts = %#v", table.columns["id"])
	}
	if got := table.columns["label"].Collation; got == nil || len(*got) != 1 || (*got)[0].Name != "utf8mb4_bin" {
		t.Fatalf("label collation = %#v", table.columns["label"].Collation)
	}
	if got := table.columns["total"].Generated; got == nil || got.Storage != "STORED" || got.Expression != "(`quantity` * (`price` + 1))" {
		t.Fatalf("total generated = %#v", got)
	}
	if got := table.columns["virtual_total"].Generated; got == nil || got.Storage != "VIRTUAL" || got.Expression != "`quantity` + (`price` * 2)" {
		t.Fatalf("virtual generated = %#v", got)
	}
	for _, name := range []string{"id", "label", "total", "virtual_total"} {
		column := table.statement.Columns[0]
		for _, candidate := range table.statement.Columns {
			if candidate.Name.Name == name {
				column = candidate
				break
			}
		}
		rendered, renderErr := renderFullColumn(columnDefinition{AST: column, Facts: table.columns[name]})
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		want := map[string]string{
			"id":            "`id` bigint NOT NULL AUTO_INCREMENT PRIMARY KEY",
			"label":         "`label` varchar(64) COLLATE `utf8mb4_bin` NOT NULL DEFAULT 'new'",
			"total":         "`total` decimal(12, 2) GENERATED ALWAYS AS ((`quantity` * (`price` + 1))) STORED",
			"virtual_total": "`virtual_total` decimal(12, 2) GENERATED ALWAYS AS (`quantity` + (`price` * 2)) VIRTUAL",
		}[name]
		if rendered != want {
			t.Errorf("rendered %s = %q, want %q", name, rendered, want)
		}
	}
	copy := cloneColumnFacts(table.columns)
	if !reflect.DeepEqual(copy, table.columns) {
		t.Fatal("cloned public facts differ")
	}
	if copy["label"].Collation == table.columns["label"].Collation || copy["total"].Generated == table.columns["total"].Generated {
		t.Fatal("public fact clone aliases pointers")
	}
	(*copy["label"].Collation)[0].Name = "changed"
	copy["total"].Generated.Expression = "changed"
	if (*table.columns["label"].Collation)[0].Name == "changed" || table.columns["total"].Generated.Expression == "changed" {
		t.Fatal("public fact clone aliases nested metadata")
	}
}

func TestScanLexerReportsOpeningOffsets(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"single quote", "CREATE TABLE t (x varchar(20) DEFAULT '", "mysql schema diff: unterminated single-quoted string at byte 38"},
		{"double quote", "CREATE TABLE t (x varchar(20) DEFAULT \"", "mysql schema diff: unterminated double-quoted string at byte 38"},
		{"identifier", "CREATE TABLE t (x varchar(20) DEFAULT `", "mysql schema diff: unterminated quoted identifier at byte 38"},
		{"closing parenthesis", "CREATE TABLE t )", "mysql schema diff: unmatched closing parenthesis at byte 15"},
		{"comment", "CREATE TABLE t (x int /*", "mysql schema diff: unterminated block comment at byte 22"},
		{"parenthesis", "CREATE TABLE t (x int", "mysql schema diff: unclosed parenthesis opened at byte 15"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lexMySQLSource(tc.source)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("lexMySQLSource error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestParseRejectsGeneratedAndCollationFactsPublicly(t *testing.T) {
	for _, tc := range []struct{ name, column, clause, want string }{
		{"empty generated", "total", "GENERATED AS () STORED", "generated column requires expression"},
		{"unexpected generated suffix", "total", "GENERATED AS (x) PERSISTENT", "generated column requires VIRTUAL or STORED"},
		{"invalid generated form", "total", "GENERATED MADEUP AS (x)", "generated column requires AS"},
		{"duplicate collate", "label", "COLLATE utf8mb4_bin COLLATE latin1_bin", "duplicate COLLATE attribute"},
		{"generated default", "total", "GENERATED AS (x) STORED DEFAULT 1", "generated column cannot combine with AUTO_INCREMENT or DEFAULT"},
		{"generated auto", "total", "GENERATED AS (x) STORED AUTO_INCREMENT", "generated column cannot combine with AUTO_INCREMENT or DEFAULT"},
		{"generated unexpected text", "total", "GENERATED AS (x) MADEUP", "generated column requires VIRTUAL or STORED"},
		{"malformed collate", "label", "COLLATE .utf8mb4_bin", "invalid COLLATE clause"},
		{"duplicate generated", "total", "GENERATED AS (x) STORED GENERATED AS (y)", "duplicate GENERATED attribute"},
		{"duplicate auto", "id", "BIGINT AUTO_INCREMENT AUTO_INCREMENT PRIMARY KEY", "duplicate AUTO_INCREMENT attribute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "CREATE TABLE facts (" + tc.column + " int " + tc.clause + ");"
			_, err := New().Parse([]diff.Source{{Path: "facts.sql", SQL: sqltext.Text(source)}})
			if err == nil || err.Error() != `mysql schema source "facts.sql": mysql schema diff: table facts column `+tc.column+`: `+tc.want {
				t.Fatalf("Parse error = %v, want table, column, and %q", err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, source, want string
	}{
		{"single quote", "CREATE TABLE facts (label varchar(10) DEFAULT 'unterminated);", `mysql schema source "facts.sql": mysql schema diff: unterminated single-quoted string at byte 46`},
		{"double quote", "CREATE TABLE facts (label varchar(10) DEFAULT \"unterminated);", `mysql schema source "facts.sql": mysql schema diff: unterminated double-quoted string at byte 46`},
		{"backtick", "CREATE TABLE facts (label varchar(10) COLLATE `utf8mb4);", `mysql schema source "facts.sql": mysql schema diff: unterminated quoted identifier at byte 46`},
		{"block comment", "CREATE TABLE facts (label varchar(10) /* unterminated);", `mysql schema source "facts.sql": mysql schema diff: unterminated block comment at byte 38`},
		{"unterminated collate", "CREATE TABLE facts (label varchar(10) COLLATE `utf8mb4_bin", `mysql schema source "facts.sql": mysql schema diff: unterminated quoted identifier at byte 46`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New().Parse([]diff.Source{{Path: "facts.sql", SQL: sqltext.Text(tc.source)}})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Parse error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseCompositeAutoIncrementLeadingColumn(t *testing.T) {
	accepted := "CREATE TABLE `keys` (`id` bigint AUTO_INCREMENT, `tenant` bigint, CONSTRAINT `keys_uq` UNIQUE (`id`, `tenant`));"
	if _, err := New().Parse([]diff.Source{{Path: "keys.sql", SQL: sqltext.Text(accepted)}}); err != nil {
		t.Fatal(err)
	}
	rejected := strings.Replace(accepted, "(`id`, `tenant`)", "(`tenant`, `id`)", 1)
	if _, err := New().Parse([]diff.Source{{Path: "keys.sql", SQL: sqltext.Text(rejected)}}); err == nil || err.Error() != "mysql schema source \"keys.sql\": auto increment column id must be the leading column of a primary or unique key" {
		t.Fatalf("non-leading auto increment error = %v", err)
	}
	duplicate := "CREATE TABLE `keys` (`a` bigint AUTO_INCREMENT PRIMARY KEY, `b` bigint AUTO_INCREMENT UNIQUE);"
	if _, err := New().Parse([]diff.Source{{Path: "keys.sql", SQL: sqltext.Text(duplicate)}}); err == nil || err.Error() != "mysql schema source \"keys.sql\": table keys has multiple AUTO_INCREMENT columns: a and b" {
		t.Fatalf("duplicate auto increment error = %v", err)
	}
}
