package schemasource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/schema"
)

// migrationHistoryTable names the migration history table migrate.New creates, copied here
// rather than imported so that a caller of Read is not tied to migrate's default remaining
// unexported; internal/catalog keeps the same copy for the same reason.
const migrationHistoryTable = "rasql_schema_migrations"

// ReadRequest describes one database to read for the new generate path: no lock, no declared
// schema source kind, just a DSN (or a scratch database to build) and an optional migration
// directory.
type ReadRequest struct {
	ModuleRoot    string
	Dialect       string
	DSN           string
	MigrationsDir string
	TempRoot      string
	Scratch       bool
	Scope         catalogread.Scope
}

// ReadResult carries no lock-record concept: nothing here is compared against or folded into a
// checked-in file.
type ReadResult struct {
	Catalog    compilerir.PhysicalCatalog
	Profile    engineprofile.Profile
	Migrations []migrate.Migration
	Queries    []compilerir.QueryAnalysis
	Unresolved []catalogread.UnresolvedFact
	Snapshots  []sourcefile.SourceFileSnapshot
}

func (r ReadResult) Clone() ReadResult {
	x := r
	x.Catalog = r.Catalog.Clone()
	x.Migrations = append([]migrate.Migration(nil), r.Migrations...)
	x.Queries = append([]compilerir.QueryAnalysis(nil), r.Queries...)
	for i := range x.Queries {
		x.Queries[i] = r.Queries[i].Clone()
	}
	x.Unresolved = append([]catalogread.UnresolvedFact(nil), r.Unresolved...)
	x.Snapshots = append([]sourcefile.SourceFileSnapshot(nil), r.Snapshots...)
	return x
}

// Read opens req.DSN (or, with Scratch, creates a throwaway database through DisposableFactory),
// discovers the engine profile from the server alone (ProfileDiscoverer, no config override),
// applies or checks any configured migration directory, reads the catalog under req.Scope, and
// runs the query analyzer against the same connection. ReadResult carries no lock record: there
// is nothing here to compare against a checked-in file.
//
// When MigrationsDir is set and Scratch is false, Read first asks the catalog whether the
// migration history table exists, and refuses - naming "rasql migrate apply -dir <dir>" as the
// fix - without calling migrate.Runner.Status at all when it does not. This matters because
// Status calls ensureHistory, which would otherwise create rasql_schema_migrations on a
// database rasql did not build the first time Read looked at it. Once the table is known to
// exist, ensureHistory is a no-op, so calling Status creates nothing new on that front either
// way. One residual case is deliberately not covered by this check: on MySQL, Status also
// ensures a "_progress" companion table exists, and if the history table is present but that
// companion is not, calling Status still creates it. Avoiding that would mean changing
// migrate.Runner.Status's contract, which is out of scope here.
func Read(ctx context.Context, req ReadRequest, deps Dependencies) (ReadResult, error) {
	if err := validateReadRequest(req); err != nil {
		return ReadResult{}, err
	}
	if err := validateReadDeps(req, deps); err != nil {
		return ReadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ReadResult{}, err
	}

	var db *sql.DB
	var cleanup func(context.Context) error
	var connectionDSN string
	created := false
	var returnResult ReadResult

	primary := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if req.Scratch {
			owned, err := deps.Factory.Create(ctx, FactoryRequest{Dialect: engineFor(req.Dialect), BootstrapDSN: req.DSN, TempRoot: req.TempRoot})
			if err != nil {
				return err
			}
			created = true
			db, cleanup, connectionDSN = owned.DB, owned.CloseAndDrop, owned.DSN
			if db == nil || cleanup == nil {
				return fmt.Errorf("schema source: disposable factory returned incomplete database")
			}
		} else {
			var err error
			db, err = deps.Opener.Open(req.Dialect, req.DSN)
			if err != nil {
				return err
			}
			if db == nil {
				return fmt.Errorf("schema source: database opener returned nil database")
			}
			connectionDSN = req.DSN
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		profile, err := deps.Discoverer.Discover(ctx, db, req.Dialect)
		if err != nil {
			return err
		}

		var migrations []migrate.Migration
		var migrationSnaps []sourcefile.SourceFileSnapshot
		if req.MigrationsDir != "" {
			normalizedDir, e := sourcefile.NormalizePath(req.MigrationsDir)
			if e != nil {
				return e
			}
			absDir := filepath.Join(req.ModuleRoot, filepath.FromSlash(normalizedDir))
			migrations, e = migrationdir.Load(absDir)
			if e != nil {
				return e
			}
			migrationSnaps, e = snapshotMigrationDir(req.ModuleRoot, normalizedDir)
			if e != nil {
				return e
			}
			if req.Scratch {
				if e := deps.Migrations.Apply(ctx, db, profile, migrations); e != nil {
					return e
				}
			} else {
				exists, e := historyTableExists(ctx, deps.Catalogs, db, profile)
				if e != nil {
					return e
				}
				if !exists {
					return fmt.Errorf("schema source: migration history table does not exist; run rasql migrate apply -dir %s", req.MigrationsDir)
				}
				statuses, e := deps.Migrations.Status(ctx, db, profile, migrations)
				if e != nil {
					return e
				}
				for _, entry := range statuses {
					if entry.State != migrate.StatusApplied {
						return fmt.Errorf("schema source: migration %q is %s; run rasql migrate apply -dir %s", entry.ID, entry.State, req.MigrationsDir)
					}
				}
			}
		}

		read, e := deps.Catalogs.Read(ctx, db, profile, req.Scope)
		if e != nil {
			return e
		}
		catalog, diagnostics := compilerir.PhysicalFromTableDefs(readEngineIdentity(req.Dialect, profile), read.Tables)
		if len(diagnostics) > 0 {
			return fmt.Errorf("schema source: catalog conversion: %s", diagnostics[0].Message)
		}

		analysis := AnalysisResult{}
		if deps.Analyzer != nil {
			analysis, err = deps.Analyzer.Analyze(ctx, AnalysisRequest{DB: db, DSN: connectionDSN, Profile: profile, Catalog: catalog.Clone()})
			if err != nil {
				return err
			}
		}
		queries := make([]compilerir.QueryAnalysis, len(analysis.Queries))
		for i := range analysis.Queries {
			queries[i] = analysis.Queries[i].Clone()
		}
		querySnapshots := append([]sourcefile.SourceFileSnapshot(nil), analysis.Snapshots...)
		sort.SliceStable(querySnapshots, func(i, j int) bool { return querySnapshots[i].Path() < querySnapshots[j].Path() })
		allSnapshots := append([]sourcefile.SourceFileSnapshot(nil), migrationSnaps...)
		allSnapshots = append(allSnapshots, querySnapshots...)

		returnResult = ReadResult{Catalog: catalog, Profile: profile, Migrations: migrations, Queries: queries, Unresolved: read.Unresolved, Snapshots: allSnapshots}
		return nil
	}

	err := primary()
	if created && cleanup != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := cleanup(closeCtx); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	} else if db != nil {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(err, ctxErr)
		}
		return ReadResult{}, err
	}
	return returnResult.Clone(), nil
}

func validateReadRequest(r ReadRequest) error {
	if !filepath.IsAbs(r.ModuleRoot) || r.ModuleRoot == "" || filepath.Clean(r.ModuleRoot) != r.ModuleRoot {
		return fmt.Errorf("schema source: module root must be absolute and clean")
	}
	switch strings.ToLower(r.Dialect) {
	case "postgresql", "postgres", "mysql", "sqlite":
	default:
		return fmt.Errorf("schema source: unsupported engine dialect %q", r.Dialect)
	}
	if r.Scratch {
		if strings.ToLower(r.Dialect) != "sqlite" && r.DSN == "" {
			return fmt.Errorf("schema source: bootstrap DSN is required for a scratch database")
		}
	} else if r.DSN == "" {
		return fmt.Errorf("schema source: DSN is required")
	}
	if r.MigrationsDir != "" {
		if _, err := sourcefile.NormalizePath(r.MigrationsDir); err != nil {
			return err
		}
	}
	return nil
}

func validateReadDeps(r ReadRequest, d Dependencies) error {
	if d.Discoverer == nil || d.Catalogs == nil {
		return fmt.Errorf("schema source: dependencies are incomplete")
	}
	if r.Scratch {
		if d.Factory == nil {
			return fmt.Errorf("schema source: dependencies are incomplete")
		}
	} else if d.Opener == nil {
		return fmt.Errorf("schema source: dependencies are incomplete")
	}
	if r.MigrationsDir != "" && d.Migrations == nil {
		return fmt.Errorf("schema source: dependencies are incomplete")
	}
	return nil
}

func readEngineIdentity(dialect string, p engineprofile.Profile) compilerir.EngineIdentity {
	return compilerir.EngineIdentity{Dialect: dialect, Version: versionString(p), Profile: p.ID}
}

// historyTableExists asks the catalog whether the migration history table exists, without
// building the physical catalog Read ultimately returns: it scopes the read to that one table
// by name and reads the "table not found" refusal catalogread.Read gives for a named Include
// that does not exist, rather than teaching a new mechanism to check existence.
func historyTableExists(ctx context.Context, catalogs CatalogReader, db catalogread.DB, p engineprofile.Profile) (bool, error) {
	_, err := catalogs.Read(ctx, db, p, catalogread.Scope{Include: []schema.ObjectName{{Name: migrationHistoryTable}}})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, engineprofile.ErrUnresolvedFact) {
		return false, nil
	}
	return false, err
}

// snapshotMigrationDir snapshots every file under moduleRoot/dir, dot-prefixed entries
// excluded at every level exactly as internal/migrationdir.Load excludes them when parsing the
// same tree. It is safe to call only after migrationdir.Load has already validated dir's
// structure.
func snapshotMigrationDir(moduleRoot, dir string) ([]sourcefile.SourceFileSnapshot, error) {
	abs := filepath.Join(moduleRoot, filepath.FromSlash(dir))
	var relPaths []string
	err := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(moduleRoot, path)
		if e != nil {
			return e
		}
		relPaths = append(relPaths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(relPaths)
	snaps := make([]sourcefile.SourceFileSnapshot, 0, len(relPaths))
	for _, p := range relPaths {
		s, e := sourcefile.SnapshotSourceFile(moduleRoot, p)
		if e != nil {
			return nil, e
		}
		snaps = append(snaps, s)
	}
	return snaps, nil
}
