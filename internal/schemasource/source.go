// Package schemasource reads a live database for code generation.
package schemasource

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/migrate"
)

type FactoryRequest struct{ Dialect, ProfileID, BootstrapDSN, TempRoot string }
type DisposableDatabase struct {
	DB           *sql.DB
	DSN          string
	CloseAndDrop func(context.Context) error
}
type DisposableFactory interface {
	Create(context.Context, FactoryRequest) (DisposableDatabase, error)
}
type DatabaseOpener interface {
	Open(dialect, dsn string) (*sql.DB, error)
}
type MigrationApplier interface {
	// Apply applies migrations to db, in order.
	Apply(ctx context.Context, db *sql.DB, p engineprofile.Profile, migrations []migrate.Migration) error
	// Status reports each migration's state in the database, the way migrate.Runner.Status does,
	// without applying anything.
	Status(ctx context.Context, db *sql.DB, p engineprofile.Profile, migrations []migrate.Migration) ([]migrate.StatusEntry, error)
}

// ProfileDiscoverer resolves an engine profile from the connected server
// alone, with no caller-supplied override: there is no engine.profile config
// key to override with (design decision: engine.profile is derived from the
// server only).
type ProfileDiscoverer interface {
	Discover(ctx context.Context, db *sql.DB, dialect string) (engineprofile.Profile, error)
}
type CatalogReader interface {
	Read(context.Context, catalogread.DB, engineprofile.Profile, catalogread.Scope) (catalogread.Result, error)
}
type AnalysisRequest struct {
	DB      *sql.DB
	DSN     string
	Profile engineprofile.Profile
}
type AnalysisResult struct {
	Queries   []compilerir.QueryAnalysis
	Snapshots []sourcefile.SourceFileSnapshot
}
type Analyzer interface {
	Analyze(context.Context, AnalysisRequest) (AnalysisResult, error)
}
// Dependencies carries the adapters Read calls. Discoverer and Catalogs must be set for every ReadRequest,
// Factory when ReadRequest.Scratch is true, Opener when it is false, and Migrations when
// ReadRequest.MigrationsDir is not empty. Read reports "schema source: dependencies are incomplete" for a
// field it needs and does not have. Analyzer may be nil, and Read then returns a ReadResult with no query
// analysis.
//
// `Discoverer` and `Catalogs` must not be nil.
type Dependencies struct {
	Factory    DisposableFactory
	Opener     DatabaseOpener
	Migrations MigrationApplier
	Catalogs   CatalogReader
	Analyzer   Analyzer
	Discoverer ProfileDiscoverer
}

// DefaultDependencies wires every production adapter Read needs.
func DefaultDependencies() Dependencies {
	return Dependencies{Factory: defaultFactory{}, Opener: sqlOpener{}, Migrations: defaultMigrations{}, Catalogs: defaultCatalogs{}, Discoverer: defaultProfileDiscoverer{}}
}

func engineFor(s string) string { return strings.ToLower(s) }

func versionString(p engineprofile.Profile) string {
	if !p.Version.Known {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", p.Version.Major, p.Version.Minor, p.Version.Patch)
}
