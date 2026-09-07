package schemasource

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestDerivedDSNReplacesPostgreSQLDatabaseAndPreservesOptions(t *testing.T) {
	for _, dsn := range []string{
		"postgres://user:p%40ss@example.test:5433/bootstrap?sslmode=disable&application_name=rasql",
		"user=user password='p ss' host=example.test port=5433 dbname=bootstrap sslmode=disable application_name=rasql",
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
		if cfg.Host != "example.test" || cfg.Port != 5433 || cfg.User != "user" || cfg.RuntimeParams["application_name"] != "rasql" {
			t.Fatalf("derived config lost options: %#v", cfg)
		}
		if strings.Contains(got, "bootstrap") {
			t.Fatalf("derived DSN retains bootstrap database: %q", got)
		}
	}
}

func TestReplaceKeywordDatabaseEscapesGeneratedName(t *testing.T) {
	got := replaceKeywordDatabase("user=u dbname=bootstrap sslmode=disable", "rasql_schema_a'b")
	if !strings.Contains(got, `dbname='rasql_schema_a\'b'`) {
		t.Fatalf("dsn=%q", got)
	}
	if _, err := pgx.ParseConfig(got); err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
}

func TestRedactErrorPreservesCause(t *testing.T) {
	cause := errors.New("contains secret")
	got := redactError(cause, "secret")
	if !errors.Is(got, cause) {
		t.Fatalf("redacted error lost cause: %v", got)
	}
	if strings.Contains(got.Error(), "secret") {
		t.Fatalf("error leaked secret: %v", got)
	}
}
