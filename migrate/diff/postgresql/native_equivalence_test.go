package postgresql

import (
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/sqltext"
)

func TestDiffTreatsInspectedBuiltinNativeSpellingAsEquivalent(t *testing.T) {
	analyzer := New()
	expected, err := analyzer.Parse([]diff.Source{{Path: "expected.sql", SQL: sqltext.Text(`CREATE TABLE tasks (created_at TIMESTAMPTZ NOT NULL DEFAULT now());`)}})
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := analyzer.Parse([]diff.Source{{Path: "inspected.sql", SQL: sqltext.Text(`CREATE TABLE tasks (created_at "pg_catalog"."timestamptz"(6) NOT NULL DEFAULT now());`)}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := analyzer.Diff(inspected, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 0 || len(plan.Decisions) != 0 || len(plan.Statements) != 0 {
		t.Fatalf("equivalent built-in type produced a plan: %#v", plan)
	}
}

func TestDiffKeepsMaterialNativeBuiltinMismatches(t *testing.T) {
	analyzer := New()
	for _, source := range []string{
		`CREATE TABLE tasks (created_at "pg_catalog"."timestamptz"(3) NOT NULL DEFAULT now());`,
		`CREATE TABLE tasks (created_at "pg_catalog"."jsonb" NOT NULL);`,
	} {
		baseline, err := analyzer.Parse([]diff.Source{{Path: "baseline.sql", SQL: sqltext.Text(source)}})
		if err != nil {
			t.Fatal(err)
		}
		targetSQL := `CREATE TABLE tasks (created_at TIMESTAMPTZ NOT NULL DEFAULT now());`
		if source == `CREATE TABLE tasks (created_at "pg_catalog"."jsonb" NOT NULL);` {
			targetSQL = `CREATE TABLE tasks (created_at JSON NOT NULL);`
		}
		target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: sqltext.Text(targetSQL)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := analyzer.Diff(baseline, target); err == nil {
			t.Fatalf("material native mismatch was accepted: %s", source)
		}
	}
}
