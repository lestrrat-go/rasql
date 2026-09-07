package schemasource_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
)

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

type fakeProfile struct {
	p     engineprofile.Profile
	calls int
}

func (f *fakeProfile) Resolve(context.Context, *sql.DB, schemasource.EngineConfig) (engineprofile.Profile, error) {
	f.calls++
	return f.p, nil
}

type fakeCatalog struct {
	calls  int
	result catalogread.Result
	err    error
}

func (f *fakeCatalog) Read(context.Context, catalogread.DB, engineprofile.Profile, catalogread.Scope) (catalogread.Result, error) {
	f.calls++
	return f.result, f.err
}

type fakeMigration struct{ calls int }

func (f *fakeMigration) Apply(context.Context, *sql.DB, engineprofile.Profile, []compilerlock.SourceFileSnapshot) error {
	f.calls++
	return nil
}

type fakeProcess struct {
	req   schemasource.ProcessRequest
	calls int
}

type fakeOpener struct{}

func (fakeOpener) Open(string, string) (*sql.DB, error) { return nil, nil }

func (f *fakeProcess) Run(_ context.Context, r schemasource.ProcessRequest) (schemasource.ProcessResult, error) {
	f.calls++
	f.req = r
	return schemasource.ProcessResult{ExitCode: 0}, nil
}

func TestValidateRequestRejectsInvalidInputsBeforeDependencies(t *testing.T) {
	base := schemasource.Request{ModuleRoot: t.TempDir(), Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "live", Identity: "schema"}, LiveDSN: "x"}
	for name, mutate := range map[string]func(*schemasource.Request){
		"identity":     func(r *schemasource.Request) { r.Source.Identity = "" },
		"kind":         func(r *schemasource.Request) { r.Source.Kind = "unknown" },
		"environment":  func(r *schemasource.Request) { r.Source.Environment = map[string]string{"RASQL_SCHEMA_DSN": "x"} },
		"live-command": func(r *schemasource.Request) { r.Source.Command = []string{"tool"} },
		"command-nul":  func(r *schemasource.Request) { r.Source.Command = []string{"tool\x00arg"} },
		"env-nul":      func(r *schemasource.Request) { r.Source.Environment = map[string]string{"MODE": "bad\x00value"} },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			mutate(&r)
			if err := schemasource.ValidateRequest(r); err == nil {
				t.Fatal("ValidateRequest returned nil")
			}
		})
	}
}

func TestValidateRequestRejectsMigrationEnvironmentBeforeFactory(t *testing.T) {
	r := schemasource.Request{
		ModuleRoot: t.TempDir(),
		Engine:     schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"},
		Source:     schemasource.SchemaSourceConfig{Kind: "migrations", Identity: "v1", Paths: []string{"migrations/*.sql"}, Environment: map[string]string{"MODE": "test"}},
	}
	if err := schemasource.ValidateRequest(r); err == nil {
		t.Fatal("ValidateRequest accepted migration environment")
	}
}

func TestMaterializeExternalClonesInputsAndJoinsCleanup(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "schema.sql")
	if err := os.WriteFile(input, []byte("CREATE TABLE users(id INTEGER);"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 40})
	if err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{db: db, dsn: "owned"}
	profiles := &fakeProfile{p: p}
	catalogs := &fakeCatalog{}
	proc := &fakeProcess{}
	req := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "external", Identity: "tool-v1", Inputs: []string{"schema.sql"}, Command: []string{"tool"}, Environment: map[string]string{"MODE": "test"}}, TempRoot: root}
	deps := schemasource.Dependencies{Factory: factory, Profiles: profiles, Catalogs: catalogs, Processes: proc}
	got, err := schemasource.Materialize(context.Background(), req, deps)
	if err != nil {
		t.Fatal(err)
	}
	if factory.create != 1 || factory.cleanup != 1 || proc.calls != 1 {
		t.Fatalf("calls create=%d cleanup=%d process=%d", factory.create, factory.cleanup, proc.calls)
	}
	if len(got.Snapshots) != 1 || got.Snapshots[0].Path() != "schema.sql" {
		t.Fatalf("unexpected snapshots: %#v", got.Snapshots)
	}
	if len(proc.req.Argv) != 1 || proc.req.Argv[0] != "tool" {
		t.Fatalf("argv mutated: %#v", proc.req.Argv)
	}
	if got.Source.Record.Identity != "tool-v1" {
		t.Fatalf("identity=%q", got.Source.Record.Identity)
	}
}

func TestMaterializeJoinsCleanupError(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, _ := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 40})
	cleanupErr := errors.New("cleanup")
	path := filepath.Join(root, "schema.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE users(id INTEGER);"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeFactory{db: db, dsn: "owned", cleanupErr: cleanupErr}
	c := &fakeCatalog{}
	r := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "external", Identity: "x", Inputs: []string{"schema.sql"}, Command: []string{"tool"}}, TempRoot: root}
	_, err = schemasource.Materialize(context.Background(), r, schemasource.Dependencies{Factory: f, Profiles: &fakeProfile{p: p}, Catalogs: c, Processes: &fakeProcess{}})
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v, want cleanup cause", err)
	}
	if f.cleanup != 1 {
		t.Fatalf("cleanup calls=%d", f.cleanup)
	}
}

func TestMaterializeJoinsPrimaryAndDetachedCleanupErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schema.sql"), []byte("schema"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, _ := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 40})
	primaryErr := errors.New("primary")
	cleanupErr := errors.New("cleanup")
	factory := &fakeFactory{db: db, dsn: "owned", cleanupErr: cleanupErr}
	r := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "external", Identity: "x", Inputs: []string{"schema.sql"}, Command: []string{"tool"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = schemasource.Materialize(ctx, r, schemasource.Dependencies{Factory: factory, Profiles: &fakeProfile{p: p}, Catalogs: &fakeCatalog{err: primaryErr}, Processes: &fakeProcess{}})
	if !errors.Is(err, primaryErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("error=%v, want both primary and cleanup causes", err)
	}
	if factory.cleanup != 1 {
		t.Fatalf("cleanup calls=%d, want one", factory.cleanup)
	}
	if factory.cleanupCtxErr != nil {
		t.Fatalf("cleanup context was canceled: %v", factory.cleanupCtxErr)
	}
}

func TestMaterializeRejectsIncompleteDisposableAndCleansAvailableHandle(t *testing.T) {
	p, _ := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 40})
	root := t.TempDir()
	path := filepath.Join(root, "schema.sql")
	if err := os.WriteFile(path, []byte("schema"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for name, factory := range map[string]*fakeFactory{
		"missing database": {db: nil, dsn: "owned"},
		"missing dsn":      {db: db, dsn: ""},
		"missing cleanup":  {db: db, dsn: "owned", noClose: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "external", Identity: "x", Inputs: []string{"schema.sql"}, Command: []string{"tool"}}}
			_, err := schemasource.Materialize(context.Background(), r, schemasource.Dependencies{Factory: factory, Profiles: &fakeProfile{p: p}, Catalogs: &fakeCatalog{}, Processes: &fakeProcess{}})
			if err == nil {
				t.Fatal("Materialize accepted incomplete disposable")
			}
			if !factory.noClose && factory.cleanup != 1 {
				t.Fatalf("cleanup calls=%d, want one", factory.cleanup)
			}
		})
	}
}

func TestMaterializeRejectsTypedNilOptionalAnalyzer(t *testing.T) {
	var analyzer *liveAnalyzer
	r := schemasource.Request{ModuleRoot: t.TempDir(), Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "live", Identity: "x"}, LiveDSN: ":memory:"}
	_, err := schemasource.Materialize(context.Background(), r, schemasource.Dependencies{Opener: fakeOpener{}, Profiles: &fakeProfile{}, Catalogs: &fakeCatalog{}, Analyzer: analyzer})
	if err == nil {
		t.Fatal("Materialize accepted typed nil analyzer")
	}
}

func TestMaterializeRejectsNilLiveDatabaseBeforeProfile(t *testing.T) {
	profile := &fakeProfile{}
	catalogs := &fakeCatalog{}
	r := schemasource.Request{ModuleRoot: t.TempDir(), Engine: schemasource.EngineConfig{Dialect: "sqlite", Profile: "sqlite-3.35"}, Source: schemasource.SchemaSourceConfig{Kind: "live", Identity: "x"}, LiveDSN: ":memory:"}
	_, err := schemasource.Materialize(context.Background(), r, schemasource.Dependencies{Opener: fakeOpener{}, Profiles: profile, Catalogs: catalogs})
	if err == nil {
		t.Fatal("Materialize accepted nil live database")
	}
	if profile.calls != 0 || catalogs.calls != 0 {
		t.Fatalf("downstream calls profile=%d catalog=%d", profile.calls, catalogs.calls)
	}
}
