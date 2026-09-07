// Package schemasource materializes declared schema sources for generation.
package schemasource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
)

type EngineConfig struct{ Dialect, Profile string }
type SchemaSourceConfig struct {
	Kind        string
	Identity    string
	Paths       []string
	Inputs      []string
	Command     []string
	Environment map[string]string
}
type Request struct {
	ModuleRoot   string
	Engine       EngineConfig
	Source       SchemaSourceConfig
	BootstrapDSN string
	LiveDSN      string
	TempRoot     string
	Scope        catalogread.Scope
}
type Result struct {
	Catalog    compilerir.PhysicalCatalog
	Profile    engineprofile.Profile
	Source     compilerlock.SourceDigestInput
	Snapshots  []compilerlock.SourceFileSnapshot
	Unresolved []catalogread.UnresolvedFact
	Queries    []compilerir.QueryAnalysis
}
type VerifyResult struct {
	Candidate   Result
	Differences []string
}

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
type ProcessRequest struct {
	Argv, Environment []string
	Directory         string
}
type ProcessResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}
type ProcessRunner interface {
	Run(context.Context, ProcessRequest) (ProcessResult, error)
}
type MigrationApplier interface {
	Apply(context.Context, *sql.DB, engineprofile.Profile, []compilerlock.SourceFileSnapshot) error
}
type ProfileResolver interface {
	Resolve(context.Context, *sql.DB, EngineConfig) (engineprofile.Profile, error)
}
type CatalogReader interface {
	Read(context.Context, catalogread.DB, engineprofile.Profile, catalogread.Scope) (catalogread.Result, error)
}
type AnalysisRequest struct {
	DB      *sql.DB
	DSN     string
	Profile engineprofile.Profile
	Catalog compilerir.PhysicalCatalog
}
type AnalysisResult struct {
	Queries   []compilerir.QueryAnalysis
	Snapshots []compilerlock.SourceFileSnapshot
}
type Analyzer interface {
	Analyze(context.Context, AnalysisRequest) (AnalysisResult, error)
}
type Dependencies struct {
	Factory    DisposableFactory
	Opener     DatabaseOpener
	Processes  ProcessRunner
	Migrations MigrationApplier
	Profiles   ProfileResolver
	Catalogs   CatalogReader
	Analyzer   Analyzer
}

func ValidateRequest(r Request) error {
	if !filepath.IsAbs(r.ModuleRoot) || r.ModuleRoot == "" || filepath.Clean(r.ModuleRoot) != r.ModuleRoot {
		return fmt.Errorf("schema source: module root must be absolute and clean")
	}
	if r.Engine.Dialect == "" || r.Engine.Profile == "" {
		return fmt.Errorf("schema source: engine dialect and profile are required")
	}
	switch strings.ToLower(r.Engine.Dialect) {
	case "postgresql", "postgres", "mysql", "sqlite":
	default:
		return fmt.Errorf("schema source: unsupported engine dialect %q", r.Engine.Dialect)
	}
	if strings.TrimSpace(r.Source.Identity) == "" {
		return fmt.Errorf("schema source: source identity is required")
	}
	if r.Source.Kind != "migrations" && r.Source.Kind != "external" && r.Source.Kind != "live" {
		return fmt.Errorf("schema source: unsupported source kind %q", r.Source.Kind)
	}
	if r.Source.Kind == "live" {
		if r.LiveDSN == "" {
			return fmt.Errorf("schema source: live DSN is required")
		}
		if r.BootstrapDSN != "" || len(r.Source.Paths) != 0 || len(r.Source.Inputs) != 0 || len(r.Source.Command) != 0 || len(r.Source.Environment) != 0 {
			return fmt.Errorf("schema source: live source cannot declare paths, inputs, command, or environment")
		}
	} else {
		if r.BootstrapDSN == "" && r.Engine.Dialect != "sqlite" {
			return fmt.Errorf("schema source: bootstrap DSN is required")
		}
		if r.Source.Kind == "migrations" && (len(r.Source.Paths) == 0 || len(r.Source.Command) != 0 || len(r.Source.Inputs) != 0 || len(r.Source.Environment) != 0 || r.LiveDSN != "") {
			return fmt.Errorf("schema source: migrations require paths and forbid command, inputs, and environment")
		}
		if r.Source.Kind == "external" && (len(r.Source.Command) == 0 || strings.TrimSpace(r.Source.Command[0]) == "" || len(r.Source.Inputs) == 0 || len(r.Source.Paths) != 0 || r.LiveDSN != "") {
			return fmt.Errorf("schema source: external requires command and inputs and forbids paths")
		}
	}
	for _, path := range r.Source.Paths {
		if _, err := compilerlock.NormalizePath(path); err != nil {
			return err
		}
	}
	seenInputs := map[string]bool{}
	for _, path := range r.Source.Inputs {
		normalized, err := compilerlock.NormalizePath(path)
		if err != nil {
			return err
		}
		if strings.ContainsAny(path, "*?[") {
			return fmt.Errorf("schema source: external input %q must be an exact file", path)
		}
		if seenInputs[normalized] {
			return fmt.Errorf("schema source: duplicate input %q", normalized)
		}
		seenInputs[normalized] = true
	}
	for k := range r.Source.Environment {
		if !validEnvKey(k) || k == "RASQL_SCHEMA_DSN" {
			return fmt.Errorf("schema source: invalid environment key %q", k)
		}
	}
	for k, v := range r.Source.Environment {
		if strings.ContainsRune(k, '\x00') || strings.ContainsRune(v, '\x00') {
			return fmt.Errorf("schema source: environment contains NUL")
		}
	}
	for _, arg := range r.Source.Command {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("schema source: command argument contains NUL")
		}
	}
	return nil
}
func validEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c != '_' && (c < 'A' || c > 'Z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func DefaultDependencies() Dependencies {
	return Dependencies{Factory: defaultFactory{}, Opener: sqlOpener{}, Processes: commandRunner{}, Migrations: defaultMigrations{}, Profiles: defaultProfiles{}, Catalogs: defaultCatalogs{}}
}

func Materialize(ctx context.Context, req Request, deps Dependencies) (Result, error) {
	if err := ValidateRequest(req); err != nil {
		return Result{}, err
	}
	if err := validateDeps(req, deps); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	paths, snaps, err := sourceSnapshots(req)
	if err != nil {
		return Result{}, err
	}
	_ = paths
	var db *sql.DB
	var profile engineprofile.Profile
	var cleanup func(context.Context) error
	var ownedConnection string
	var returnResult Result
	created := false
	primary := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if req.Source.Kind == "live" {
			db, err = deps.Opener.Open(req.Engine.Dialect, req.LiveDSN)
			if err != nil {
				return err
			}
			if db == nil {
				return fmt.Errorf("schema source: database opener returned nil database")
			}
		} else {
			d := engineFor(req.Engine.Dialect)
			owned, e := deps.Factory.Create(ctx, FactoryRequest{Dialect: d, ProfileID: req.Engine.Profile, BootstrapDSN: req.BootstrapDSN, TempRoot: req.TempRoot})
			if e != nil {
				return e
			}
			created = true
			db, cleanup, ownedConnection = owned.DB, owned.CloseAndDrop, owned.DSN
			if db == nil || strings.TrimSpace(ownedConnection) == "" || cleanup == nil {
				return fmt.Errorf("schema source: disposable factory returned incomplete database")
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		profile, err = deps.Profiles.Resolve(ctx, db, req.Engine)
		if err != nil {
			return err
		}
		if req.Source.Kind == "migrations" {
			if err = deps.Migrations.Apply(ctx, db, profile, snaps); err != nil {
				return err
			}
		}
		if req.Source.Kind == "external" {
			result, e := deps.Processes.Run(ctx, externalRequest(req, ownedConnection))
			if e != nil {
				return redactError(e, req.BootstrapDSN, ownedConnection)
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("schema source: external command exited with status %d: %s", result.ExitCode, redactMany(string(result.Stderr), req.BootstrapDSN, ownedConnection))
			}
		}
		read, e := deps.Catalogs.Read(ctx, db, profile, req.Scope)
		if e != nil {
			return e
		}
		catalog, diagnostics := compilerir.PhysicalFromTableDefs(engineIdentity(req, profile), read.Tables)
		if len(diagnostics) > 0 {
			return fmt.Errorf("schema source: catalog conversion: %s", diagnostics[0].Message)
		}
		analysis := AnalysisResult{}
		if deps.Analyzer != nil {
			analysisDSN := ownedConnection
			if req.Source.Kind == "live" {
				analysisDSN = req.LiveDSN
			}
			analysis, err = deps.Analyzer.Analyze(ctx, AnalysisRequest{DB: db, DSN: analysisDSN, Profile: profile, Catalog: catalog.Clone()})
			if err != nil {
				return err
			}
		}
		queries := append([]compilerir.QueryAnalysis(nil), analysis.Queries...)
		allSnapshots := append([]compilerlock.SourceFileSnapshot(nil), snaps...)
		allSnapshots = append(allSnapshots, analysis.Snapshots...)
		source := compilerlock.SourceDigestInput{Record: compilerlock.SourceRecord{Kind: req.Source.Kind, Identity: req.Source.Identity}, Engine: compilerlock.EngineRecord{Dialect: req.Engine.Dialect, Version: versionString(profile), Profile: profile.ID}, Materializer: materializer(req)}
		for _, s := range snaps {
			source.Record.Files = append(source.Record.Files, s.Record())
		}
		returnResult = Result{Catalog: catalog, Profile: profile, Source: source, Snapshots: allSnapshots, Unresolved: read.Unresolved, Queries: queries}
		return nil
	}
	err = primary()
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
		return Result{}, err
	}
	return returnResult.Clone(), nil
}

func validateDeps(r Request, d Dependencies) error {
	if d.Analyzer != nil && isNil(d.Analyzer) {
		return fmt.Errorf("schema source: analyzer is typed nil")
	}
	if r.Source.Kind == "live" {
		if isNil(d.Opener) || isNil(d.Profiles) || isNil(d.Catalogs) {
			return fmt.Errorf("schema source: live dependencies are incomplete")
		}
	} else if isNil(d.Factory) || isNil(d.Profiles) || isNil(d.Catalogs) || r.Source.Kind == "migrations" && isNil(d.Migrations) || r.Source.Kind == "external" && isNil(d.Processes) {
		return fmt.Errorf("schema source: dependencies are incomplete")
	}
	return nil
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}
func engineFor(s string) string { return strings.ToLower(s) }
func engineIdentity(r Request, p engineprofile.Profile) compilerir.EngineIdentity {
	return compilerir.EngineIdentity{Dialect: r.Engine.Dialect, Version: versionString(p), Profile: p.ID}
}
func versionString(p engineprofile.Profile) string {
	if !p.Version.Known {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", p.Version.Major, p.Version.Minor, p.Version.Patch)
}
func materializer(r Request) []compilerlock.KeyValue {
	out := []compilerlock.KeyValue{{Key: "profile", Value: r.Engine.Profile}}
	if r.Source.Kind == "external" {
		out = append(out, compilerlock.KeyValue{Key: "command", Value: strings.Join(r.Source.Command, "\x00")})
	}
	for k, v := range r.Source.Environment {
		out = append(out, compilerlock.KeyValue{Key: "env." + k, Value: v})
	}
	return out
}

func redactError(err error, secrets ...string) error {
	if err == nil {
		return err
	}
	return &redactedError{message: redactMany(err.Error(), secrets...), cause: err}
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }
func sourceSnapshots(r Request) ([]string, []compilerlock.SourceFileSnapshot, error) {
	var paths []string
	switch r.Source.Kind {
	case "migrations":
		for _, pattern := range r.Source.Paths {
			matches, err := filepath.Glob(filepath.Join(r.ModuleRoot, filepath.FromSlash(pattern)))
			if err != nil || len(matches) == 0 {
				return nil, nil, fmt.Errorf("schema source: migration glob %q matched no files", pattern)
			}
			for _, m := range matches {
				rel, e := filepath.Rel(r.ModuleRoot, m)
				if e != nil {
					return nil, nil, e
				}
				paths = append(paths, filepath.ToSlash(rel))
			}
		}
	case "external":
		paths = append(paths, r.Source.Inputs...)
	}
	sort.Strings(paths)
	uniq := paths[:0]
	for _, p := range paths {
		if r.Source.Kind == "external" && strings.ContainsAny(p, "*?[") {
			return nil, nil, fmt.Errorf("schema source: external input %q must be an exact file", p)
		}
		n, e := compilerlock.NormalizePath(p)
		if e != nil {
			return nil, nil, e
		}
		if len(uniq) > 0 && uniq[len(uniq)-1] == n && r.Source.Kind == "external" {
			return nil, nil, fmt.Errorf("schema source: duplicate input %q", n)
		}
		if len(uniq) > 0 && uniq[len(uniq)-1] == n {
			continue
		}
		uniq = append(uniq, n)
	}
	paths = uniq
	snaps := make([]compilerlock.SourceFileSnapshot, 0, len(paths))
	for _, p := range paths {
		s, e := compilerlock.SnapshotSourceFile(r.ModuleRoot, p)
		if e != nil {
			return nil, nil, e
		}
		snaps = append(snaps, s)
	}
	return paths, snaps, nil
}
