package schemasource_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/migrate"
)

type fakeDiscoverer struct {
	p     engineprofile.Profile
	err   error
	calls int
}

func (f *fakeDiscoverer) Discover(context.Context, *sql.DB, string) (engineprofile.Profile, error) {
	f.calls++
	return f.p, f.err
}

type fakeDBOpener struct{ db *sql.DB }

func (f fakeDBOpener) Open(string, string) (*sql.DB, error) { return f.db, nil }

// historyAwareCatalog distinguishes the history-table existence check Read makes before calling
// migrate.Runner.Status (a Scope whose sole Include entry names the migration history table)
// from the scoped read it makes afterward to build the generated catalog, so a fixture test can
// control each independently.
type historyAwareCatalog struct {
	historyExists bool
	scopedResult  catalogread.Result
	scopedErr     error
	historyCalls  int
	scopedCalls   int
}

func (c *historyAwareCatalog) Read(_ context.Context, _ catalogread.DB, _ engineprofile.Profile, scope catalogread.Scope) (catalogread.Result, error) {
	if len(scope.Include) == 1 && scope.Include[0].Name == "rasql_schema_migrations" {
		c.historyCalls++
		if c.historyExists {
			return catalogread.Result{}, nil
		}
		return catalogread.Result{}, errors.Join(engineprofile.ErrUnresolvedFact, fmt.Errorf("table not found"))
	}
	c.scopedCalls++
	return c.scopedResult, c.scopedErr
}

func writeMigration(t *testing.T, root, id, upSQL string) {
	t.Helper()
	dir := filepath.Join(root, "migrations", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.up.sql"), []byte(upSQL), 0600); err != nil {
		t.Fatal(err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestReadRequiresDialect(t *testing.T) {
	req := schemasource.ReadRequest{ModuleRoot: t.TempDir(), DSN: "x"}
	if _, err := schemasource.Read(context.Background(), req, schemasource.Dependencies{}); err == nil {
		t.Fatal("Read returned nil error for an empty dialect")
	}
}

func TestReadAppliesEveryMigrationOnScratch(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", "CREATE TABLE t(id int)")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	factory := &fakeFactory{db: db, dsn: "owned"}
	migrations := &fakeMigration{}
	req := schemasource.ReadRequest{ModuleRoot: root, Dialect: "sqlite", DSN: "bootstrap", MigrationsDir: "migrations", Scratch: true, TempRoot: root}
	deps := schemasource.Dependencies{Factory: factory, Migrations: migrations, Discoverer: &fakeDiscoverer{p: liveProfile(t)}, Catalogs: &fakeCatalog{}}
	got, err := schemasource.Read(context.Background(), req, deps)
	if err != nil {
		t.Fatal(err)
	}
	if migrations.calls != 1 {
		t.Fatalf("Apply calls = %d, want 1", migrations.calls)
	}
	if migrations.statusCalls != 0 {
		t.Fatalf("Status calls = %d, want 0 on a scratch read", migrations.statusCalls)
	}
	if len(got.Migrations) != 1 || got.Migrations[0].ID != "001_initial" {
		t.Fatalf("Migrations = %#v", got.Migrations)
	}
	if len(got.Snapshots) != 1 || got.Snapshots[0].Path() != "migrations/001_initial/1.up.sql" {
		t.Fatalf("Snapshots = %#v", got.Snapshots)
	}
	if factory.create != 1 || factory.cleanup != 1 {
		t.Fatalf("factory create=%d cleanup=%d, want 1 and 1", factory.create, factory.cleanup)
	}
}

func TestReadRefusesMissingHistoryTableWithoutCallingStatus(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", "CREATE TABLE t(id int)")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	migrations := &fakeMigration{}
	catalogs := &historyAwareCatalog{historyExists: false}
	req := schemasource.ReadRequest{ModuleRoot: root, Dialect: "sqlite", DSN: "some-dsn", MigrationsDir: "migrations"}
	deps := schemasource.Dependencies{Opener: fakeDBOpener{db: db}, Migrations: migrations, Discoverer: &fakeDiscoverer{p: liveProfile(t)}, Catalogs: catalogs}
	_, err = schemasource.Read(context.Background(), req, deps)
	if err == nil {
		t.Fatal("Read returned nil error for a missing history table")
	}
	if migrations.statusCalls != 0 {
		t.Fatalf("Status calls = %d, want 0 when the history table is missing", migrations.statusCalls)
	}
	if catalogs.historyCalls != 1 || catalogs.scopedCalls != 0 {
		t.Fatalf("historyCalls=%d scopedCalls=%d, want 1 and 0", catalogs.historyCalls, catalogs.scopedCalls)
	}
	if !containsAll(err.Error(), "rasql migrate apply -dir migrations") {
		t.Fatalf("error = %q, want it to name the fix", err.Error())
	}
}

func TestReadRefusesPendingMigrationOnNonScratch(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", "CREATE TABLE t(id int)")
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	migrations := &fakeMigration{statuses: []migrate.StatusEntry{{ID: "001_initial", State: migrate.StatusPending}}}
	catalogs := &historyAwareCatalog{historyExists: true}
	req := schemasource.ReadRequest{ModuleRoot: root, Dialect: "sqlite", DSN: "some-dsn", MigrationsDir: "migrations"}
	deps := schemasource.Dependencies{Opener: fakeDBOpener{db: db}, Migrations: migrations, Discoverer: &fakeDiscoverer{p: liveProfile(t)}, Catalogs: catalogs}
	_, err = schemasource.Read(context.Background(), req, deps)
	if err == nil {
		t.Fatal("Read returned nil error for a pending migration")
	}
	if migrations.statusCalls != 1 {
		t.Fatalf("Status calls = %d, want 1", migrations.statusCalls)
	}
	if !containsAll(err.Error(), "001_initial", "pending", "rasql migrate apply -dir migrations") {
		t.Fatalf("error = %q, want it to name the migration, its state, and the fix", err.Error())
	}
}

func TestReadDropsScratchDatabaseWhenGenerationFails(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	factory := &fakeFactory{db: db, dsn: "owned"}
	catalogs := &fakeCatalog{err: fmt.Errorf("boom")}
	req := schemasource.ReadRequest{ModuleRoot: root, Dialect: "sqlite", DSN: "bootstrap", Scratch: true, TempRoot: root}
	deps := schemasource.Dependencies{Factory: factory, Discoverer: &fakeDiscoverer{p: liveProfile(t)}, Catalogs: catalogs}
	_, err = schemasource.Read(context.Background(), req, deps)
	if err == nil {
		t.Fatal("Read returned nil error")
	}
	if factory.create != 1 || factory.cleanup != 1 {
		t.Fatalf("factory create=%d cleanup=%d, want 1 and 1 even on failure", factory.create, factory.cleanup)
	}
}
