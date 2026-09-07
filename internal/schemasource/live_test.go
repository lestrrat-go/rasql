package schemasource_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/schema"
)

func TestDefaultDependenciesProvideAllProductionAdapters(t *testing.T) {
	deps := schemasource.DefaultDependencies()
	if deps.Factory == nil || deps.Opener == nil || deps.Processes == nil || deps.Migrations == nil || deps.Profiles == nil || deps.Catalogs == nil {
		t.Fatalf("default dependencies are incomplete: %#v", deps)
	}
	if deps.Analyzer != nil {
		t.Fatal("default analyzer must remain optional")
	}
}

func TestDefaultSQLiteFactoryCreatesAndRemovesOwnedDatabase(t *testing.T) {
	deps := schemasource.DefaultDependencies()
	root := t.TempDir()
	owned, err := deps.Factory.Create(context.Background(), schemasource.FactoryRequest{Dialect: "sqlite", ProfileID: "sqlite-3.35", TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if owned.DB == nil || owned.DSN == "" {
		t.Fatalf("incomplete owned database: %#v", owned)
	}
	if _, err := os.Stat(owned.DSN); err != nil {
		t.Fatalf("owned SQLite file does not exist: %v", err)
	}
	if err := owned.CloseAndDrop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned.DSN); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned SQLite file remains, stat error=%v", err)
	}
}

func TestDefaultSQLiteMigrationsMaterializeAndVerifyEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "migrations"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "migrations", "001_create_users.sql"), []byte("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL);"), 0600); err != nil {
		t.Fatal(err)
	}
	req := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "migrations", Identity: "migrations-v1", Paths: []string{"migrations/*.sql"}}, TempRoot: filepath.Join(root, "owned"), Scope: catalogread.Scope{HistoryTable: schema.ObjectName{Name: "rasql_schema_migrations"}}}
	deps := schemasource.DefaultDependencies()
	got, err := schemasource.Materialize(context.Background(), req, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Catalog.Objects) != 1 || got.Catalog.Objects[0].Name != "users" {
		t.Fatalf("catalog=%#v", got.Catalog.Objects)
	}
	expected := compilerlock.File{Source: got.Source.Record, Engine: got.Source.Engine, Catalog: compilerlock.FromPhysical(got.Catalog)}
	verified, err := schemasource.Verify(context.Background(), req, deps, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Differences) != 0 {
		t.Fatalf("verification differences=%#v", verified.Differences)
	}
}

func TestPostgreSQLDefaultFactoryMaterializeAndVerify(t *testing.T) {
	runDefaultServerMaterializeVerify(t, "postgresql", "postgresql-17", "RASQL_TEST_POSTGRES_DSN")
}

func TestMySQLDefaultFactoryMaterializeAndVerify(t *testing.T) {
	runDefaultServerMaterializeVerify(t, "mysql", "mysql-8.4", "RASQL_TEST_MYSQL_DSN")
}

func runDefaultServerMaterializeVerify(t *testing.T, dialect, profile, envName string) {
	t.Helper()
	bootstrap := strings.TrimSpace(os.Getenv(envName))
	if bootstrap == "" {
		t.Skipf("%s is not set; export the repository service DSN to run this live acceptance test", envName)
	}
	assertDisposableTarget(t, dialect, bootstrap, profile)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "migrations"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "migrations", "001_create_users.sql"), []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);"), 0600); err != nil {
		t.Fatal(err)
	}
	req := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: dialect, Profile: profile}, Source: schemasource.SchemaSourceConfig{Kind: "migrations", Identity: "live-acceptance-v1", Paths: []string{"migrations/*.sql"}}, BootstrapDSN: bootstrap, TempRoot: filepath.Join(root, "owned"), Scope: catalogread.Scope{HistoryTable: schema.ObjectName{Name: "rasql_schema_migrations"}}}
	deps := schemasource.DefaultDependencies()
	got, err := schemasource.Materialize(t.Context(), req, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Catalog.Objects) != 1 || got.Catalog.Objects[0].Name != "users" {
		t.Fatalf("catalog=%#v", got.Catalog.Objects)
	}
	expected := compilerlock.File{Source: got.Source.Record, Engine: got.Source.Engine, Catalog: compilerlock.FromPhysical(got.Catalog)}
	verified, err := schemasource.Verify(t.Context(), req, deps, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Differences) != 0 {
		t.Fatalf("verification differences=%#v", verified.Differences)
	}
}

func assertDisposableTarget(t *testing.T, dialect, bootstrap, profile string) {
	t.Helper()
	deps := schemasource.DefaultDependencies()
	owned, err := deps.Factory.Create(t.Context(), schemasource.FactoryRequest{Dialect: dialect, ProfileID: profile, BootstrapDSN: bootstrap, TempRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owned.CloseAndDrop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}()
	var got string
	query := "SELECT DATABASE()"
	if dialect == "postgresql" {
		query = "SELECT current_database()"
	}
	if err := owned.DB.QueryRowContext(t.Context(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var want, bootstrapDB string
	if dialect == "postgresql" {
		ownedConfig, err := pgx.ParseConfig(owned.DSN)
		if err != nil {
			t.Fatal(err)
		}
		bootstrapConfig, err := pgx.ParseConfig(bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		want, bootstrapDB = ownedConfig.Database, bootstrapConfig.Database
	} else {
		ownedConfig, err := mysql.ParseDSN(owned.DSN)
		if err != nil {
			t.Fatal(err)
		}
		bootstrapConfig, err := mysql.ParseDSN(bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		want, bootstrapDB = ownedConfig.DBName, bootstrapConfig.DBName
	}
	if got != want || got == bootstrapDB {
		t.Fatalf("disposable target=%q, want generated %q distinct from bootstrap %q", got, want, bootstrapDB)
	}
}

func TestDefaultCommandRunnerExecutesWithEnvironmentAndBoundsDiagnostics(t *testing.T) {
	deps := schemasource.DefaultDependencies()
	result, err := deps.Processes.Run(context.Background(), schemasource.ProcessRequest{
		Argv:        []string{"sh", "-c", "printf '%s' \"$RASQL_SCHEMA_DSN\"; i=0; while [ $i -lt 1200000 ]; do printf x >&2; i=$((i+1)); done"},
		Environment: []string{"PATH=/bin", "RASQL_SCHEMA_DSN=owned-secret"},
		Directory:   t.TempDir(),
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("command result=%#v err=%v", result, err)
	}
	if string(result.Stdout) != "owned-secret" {
		t.Fatalf("stdout=%q", result.Stdout)
	}
	if len(result.Stderr) != 1<<20 {
		t.Fatalf("stderr length=%d, want bounded 1048576", len(result.Stderr))
	}
}

func TestDefaultCommandRunnerWaitsForCancellation(t *testing.T) {
	deps := schemasource.DefaultDependencies()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := deps.Processes.Run(ctx, schemasource.ProcessRequest{Argv: []string{"sleep", "10"}, Environment: []string{"PATH=/bin"}, Directory: t.TempDir()})
	if err == nil {
		t.Fatalf("cancellation error=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("command runner returned too late: %s", time.Since(started))
	}
}

func TestAnalyzerSeesOpenDatabaseAndClonedCatalogBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schema.sql"), []byte("schema"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	analyzer := &liveAnalyzer{queries: []compilerir.QueryAnalysis{{ID: "q", Name: "users"}}}
	factory := &fakeFactory{db: db, dsn: "owned-dsn"}
	req := liveRequest(root, "external")
	result, err := schemasource.Materialize(context.Background(), req, schemasource.Dependencies{
		Factory: factory, Profiles: &fakeProfile{p: liveProfile(t)}, Catalogs: &fakeCatalog{}, Processes: &fakeProcess{}, Analyzer: analyzer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analyzer.calls != 1 || analyzer.request.DB == nil || analyzer.request.DSN != "owned-dsn" {
		t.Fatalf("analyzer request=%#v calls=%d", analyzer.request, analyzer.calls)
	}
	if factory.cleanup != 1 {
		t.Fatalf("cleanup calls=%d", factory.cleanup)
	}
	if len(result.Queries) != 1 || result.Queries[0].ID != "q" {
		t.Fatalf("queries=%#v", result.Queries)
	}
}

func TestResultCloneDeeplyIndependentlyCopiesEvidence(t *testing.T) {
	r := schemasource.Result{
		Catalog: compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: "sqlite"}, Objects: []compilerir.PhysicalObject{{Name: "users", Columns: []compilerir.PhysicalColumn{{Name: "id"}}}}},
		Source:  compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: "external", Identity: "v1", Files: []compilerlock.SourceFile{{Path: "schema.sql"}}}, Materializer: []compilerlock.KeyValue{{Key: "a", Value: "b"}}},
		Queries: []compilerir.QueryAnalysis{{ID: "q", Parameters: []compilerir.SemanticValue{{Name: "id"}}, Diagnostics: []compilerir.Diagnostic{{Code: "d"}}}},
	}
	clone := r.Clone()
	clone.Catalog.Engine.Dialect = "mysql"
	clone.Catalog.Objects[0].Columns[0].Name = "changed"
	clone.Source.Record.Files[0].Path = "changed"
	clone.Source.Materializer[0].Value = "changed"
	clone.Queries[0].Parameters[0].Name = "changed"
	clone.Queries[0].Diagnostics[0].Code = "changed"
	if r.Catalog.Engine.Dialect != "sqlite" || r.Catalog.Objects[0].Columns[0].Name != "id" || r.Source.Record.Files[0].Path != "schema.sql" || r.Source.Materializer[0].Value != "b" || r.Queries[0].Parameters[0].Name != "id" || r.Queries[0].Diagnostics[0].Code != "d" {
		t.Fatal("Result.Clone shares mutable state")
	}
	if reflect.DeepEqual(r, clone) {
		t.Fatal("clone mutation did not change clone")
	}
}

func TestVerifyReturnsSortedUniqueEvidenceDifferencesAndDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schema.sql"), []byte("schema"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	req := liveRequest(root, "external")
	deps := schemasource.Dependencies{Factory: &fakeFactory{db: db, dsn: "owned"}, Profiles: &fakeProfile{p: liveProfile(t)}, Catalogs: &fakeCatalog{}, Processes: &fakeProcess{}}
	expected := compilerlock.File{Source: compilerlock.SourceRecord{Kind: "migrations", Identity: "different"}, Engine: compilerlock.EngineRecord{Dialect: "mysql", Profile: "other", Version: "0.0.0"}}
	verified, err := schemasource.Verify(context.Background(), req, deps, expected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(verified.Differences, ",") != "catalog,engine,source" {
		t.Fatalf("differences=%#v", verified.Differences)
	}
	if len(verified.Differences) != len(uniqueStrings(verified.Differences)) {
		t.Fatalf("differences contain duplicates: %#v", verified.Differences)
	}
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

type liveAnalyzer struct {
	calls   int
	request schemasource.AnalysisRequest
	queries []compilerir.QueryAnalysis
}

func (f *liveAnalyzer) Analyze(_ context.Context, r schemasource.AnalysisRequest) ([]compilerir.QueryAnalysis, error) {
	f.calls++
	f.request = r
	return append([]compilerir.QueryAnalysis(nil), f.queries...), nil
}

func liveProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 40})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func liveRequest(root, kind string) schemasource.Request {
	r := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: kind, Identity: "fixture-v1"}, TempRoot: root}
	if kind == "external" {
		r.Source.Inputs = []string{"schema.sql"}
		r.Source.Command = []string{"fixture-materializer"}
	}
	return r
}
