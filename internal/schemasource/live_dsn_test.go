package schemasource

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestDerivedDSNReplacesPostgreSQLDatabaseAndPreservesOptions(t *testing.T) {
	for _, dsn := range []string{
		"postgres://user:p%40ss@example.test:5433/bootstrap?sslmode=disable&application_name=rasql",
		`user='user dbname=ignored\\value' password='p dbname=ignored\\value' host=example.test port=5433 dbname=bootstrap sslmode=disable application_name=rasql`,
	} {
		got, err := derivedDSN("postgresql", dsn, "rasql_schema_a/b")
		if err != nil {
			t.Fatalf("derivedDSN(%q): %v", dsn, err)
		}
		cfg, err := pgx.ParseConfig(got)
		if err != nil {
			t.Fatalf("parse derived DSN %q: %v", got, err)
		}
		if cfg.Database != "rasql_schema_a/b" {
			t.Fatalf("database=%q, want generated database", cfg.Database)
		}
		if cfg.Host != "example.test" || cfg.Port != 5433 || !strings.Contains(cfg.User, "user") || cfg.RuntimeParams["application_name"] != "rasql" {
			t.Fatalf("derived config lost options: %#v", cfg)
		}
	}
}

func TestReplaceKeywordDatabaseEscapesGeneratedName(t *testing.T) {
	got := replaceKeywordDatabase("user=u dbname=bootstrap sslmode=disable", "rasql_schema_a'b\\c")
	if !strings.Contains(got, `dbname='rasql_schema_a\'b\\c'`) {
		t.Fatalf("dsn=%q", got)
	}
	if _, err := pgx.ParseConfig(got); err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg, err := pgx.ParseConfig(got)
	if err != nil || cfg.Database != "rasql_schema_a'b\\c" {
		t.Fatalf("database=%q, err=%v", cfg.Database, err)
	}
}

