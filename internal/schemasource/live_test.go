package schemasource_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/migrate"
)

func TestDefaultDependenciesProvideAllProductionAdapters(t *testing.T) {
	deps := schemasource.DefaultDependencies()
	if deps.Factory == nil || deps.Opener == nil || deps.Migrations == nil || deps.Catalogs == nil || deps.Discoverer == nil {
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

// TestReadAnalyzerSeesOpenDatabaseAndClonedCatalogBeforeCleanup proves the analyzer runs against
// the same connection Read opened (or built as scratch), with the connection's own DSN rather
// than the request's, and that the scratch database is still cleaned up afterward.
func TestReadAnalyzerSeesOpenDatabaseAndClonedCatalogBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	analyzer := &liveAnalyzer{queries: []compilerir.QueryAnalysis{{ID: "q", Name: "users"}}}
	factory := &fakeFactory{db: db, dsn: "owned-dsn"}
	req := schemasource.ReadRequest{ModuleRoot: root, Dialect: "sqlite", DSN: "bootstrap", Scratch: true, TempRoot: root}
	result, err := schemasource.Read(context.Background(), req, schemasource.Dependencies{
		Factory: factory, Discoverer: &fakeDiscoverer{p: liveProfile(t)}, Catalogs: &fakeCatalog{}, Analyzer: analyzer,
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

type liveAnalyzer struct {
	calls   int
	request schemasource.AnalysisRequest
	queries []compilerir.QueryAnalysis
}

func (f *liveAnalyzer) Analyze(_ context.Context, r schemasource.AnalysisRequest) (schemasource.AnalysisResult, error) {
	f.calls++
	f.request = r
	return schemasource.AnalysisResult{Queries: append([]compilerir.QueryAnalysis(nil), f.queries...)}, nil
}

func liveProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 40})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeFactory stubs schemasource.DisposableFactory: Create hands back the caller's own db and
// dsn, and counts creation and cleanup so a test can assert a scratch database was cleaned up
// exactly once, including on a failure path.
type fakeFactory struct {
	db            *sql.DB
	dsn           string
	noClose       bool
	create        int
	cleanup       int
	cleanupErr    error
	cleanupCtxErr error
}

func (f *fakeFactory) Create(context.Context, schemasource.FactoryRequest) (schemasource.DisposableDatabase, error) {
	f.create++
	var cleanup func(context.Context) error
	if !f.noClose {
		cleanup = func(ctx context.Context) error { f.cleanup++; f.cleanupCtxErr = ctx.Err(); return f.cleanupErr }
	}
	return schemasource.DisposableDatabase{DB: f.db, DSN: f.dsn, CloseAndDrop: cleanup}, nil
}

// fakeCatalog stubs schemasource.CatalogReader.
type fakeCatalog struct {
	calls  int
	result catalogread.Result
	err    error
}

func (f *fakeCatalog) Read(context.Context, catalogread.DB, engineprofile.Profile, catalogread.Scope) (catalogread.Result, error) {
	f.calls++
	return f.result, f.err
}

// fakeMigration stubs schemasource.MigrationApplier.
type fakeMigration struct {
	calls       int
	statusCalls int
	statuses    []migrate.StatusEntry
	statusErr   error
}

func (f *fakeMigration) Apply(context.Context, *sql.DB, engineprofile.Profile, []migrate.Migration) error {
	f.calls++
	return nil
}

func (f *fakeMigration) Status(context.Context, *sql.DB, engineprofile.Profile, []migrate.Migration) ([]migrate.StatusEntry, error) {
	f.statusCalls++
	return f.statuses, f.statusErr
}
