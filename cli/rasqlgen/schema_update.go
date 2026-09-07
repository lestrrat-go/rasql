package rasqlgen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/schemasource"
)

func (c command) runSchemaUpdate(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "schema update")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "bootstrap connection string")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Engine == nil || cfg.Schema == nil {
		return errors.New("schema update: config requires engine and schema")
	}
	if cfg.Package == "" || cfg.Output == "" {
		return errors.New("schema update: config requires package and output")
	}
	root, err := moduleRootForConfig(*configPath)
	if err != nil {
		return err
	}
	if _, markerErr := os.Stat(filepath.Join(root, pendingMarkerName)); markerErr == nil {
		if err := validatePending(root); err != nil {
			return err
		}
		return errors.New("schema update: pending publication requires retry after re-planning")
	}
	request := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: cfg.Engine.Dialect, Profile: cfg.Engine.Profile}, Source: schemasource.SchemaSourceConfig{Kind: cfg.Schema.Kind, Identity: cfg.Schema.Identity, Paths: cfg.Schema.Paths, Inputs: cfg.Schema.Inputs, Command: cfg.Schema.Command, Environment: cfg.Schema.Environment}, BootstrapDSN: *dsn, TempRoot: filepath.Join(root, ".tmp")}
	result, err := schemasource.Materialize(context.Background(), request, schemasource.DefaultDependencies())
	if err != nil {
		return fmt.Errorf("schema update: %w", err)
	}
	result.Catalog, _ = compilerir.AssignObjectIDs(result.Catalog, compilerir.IdentityInput{SourceIdentity: result.Source.Record.Identity})
	mappings, err := cfg.mappings()
	if err != nil {
		return err
	}
	semantic, diagnostics := compilerir.BuildSemantic(result.Catalog, mappings, result.Queries)
	if hasErrors(diagnostics) {
		return fmt.Errorf("schema update: semantic analysis failed")
	}
	generation := compilerir.GoConfig{Package: cfg.Package, Output: cfg.Output, Emitter: "legacy", Prune: true}
	goModel, diagnostics := compilerir.BuildGo(semantic, generation)
	if hasErrors(diagnostics) {
		return fmt.Errorf("schema update: Go model failed")
	}
	for _, object := range goModel.Objects {
		row := object.Row.Name
		if row != "" {
			row = strings.ToUpper(row[:1]) + row[1:]
		}
		generation.Objects = append(generation.Objects, compilerir.ObjectGoName{ID: object.ID, Source: object.SourceName, Row: object.Row.Name, File: object.SourceName + "_gen.go"})
		generation.Objects[len(generation.Objects)-1].Row = row
	}
	for _, query := range goModel.Queries {
		generation.Queries = append(generation.Queries, compilerir.QueryGoName{ID: query.ID, Function: query.Name, Result: resultName(query), Projection: query.ProjectionName})
	}
	input, err := generate.NewEmitterInput(result.Catalog, semantic, goModel, generation)
	if err != nil {
		return err
	}
	store, err := generate.LegacyStore(input)
	if err != nil {
		return err
	}
	plan, err := store.Plan()
	if err != nil {
		return err
	}
	lock := compilerlock.File{Format: compilerlock.FormatVersion, Compiler: "rasql", Source: result.Source.Record, Engine: result.Source.Engine, Catalog: compilerlock.CatalogFromPhysical(result.Catalog), Generation: compilerlock.GenerationRecord{Package: generation.Package, Output: generation.Output, Emitter: generation.Emitter, Prune: generation.Prune}}
	for _, query := range result.Queries {
		lock.Queries = append(lock.Queries, compilerlock.QueryFromAnalysis(query))
	}
	for _, object := range generation.Objects {
		lock.Generation.Objects = append(lock.Generation.Objects, compilerlock.ObjectNameRecord{ID: string(object.ID), Source: object.Source, Row: object.Row, Create: object.Create, Patch: object.Patch, File: object.File})
	}
	for _, query := range generation.Queries {
		lock.Generation.Queries = append(lock.Generation.Queries, compilerlock.QueryNameRecord{ID: string(query.ID), Function: query.Function, Result: query.Result, Projection: query.Projection, Decoder: query.Decoder, File: query.File})
	}
	lock.Digests, err = compilerlock.BuildDigests(compilerlock.DigestInputs{Source: result.Source, Mappings: mappings, Queries: queryInputs(result.Queries), Generation: generation})
	if err != nil {
		return err
	}
	encoded, err := compilerlock.Encode(lock)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(root, "rasql.lock.json")
	entries := make([]pendingEntry, 0, len(plan.Files())+1)
	for _, file := range plan.Files() {
		path, err := filepath.Rel(root, file.Path)
		if err != nil {
			return err
		}
		old, err := stateFor(file.Path)
		if err != nil {
			return err
		}
		desiredHash := sha256.Sum256(file.Source)
		entries = append(entries, pendingEntry{Path: filepath.ToSlash(path), Old: old, Desired: fileState{State: "present", SHA256: hex.EncodeToString(desiredHash[:])}})
	}
	oldLock, err := stateFor(lockPath)
	if err != nil {
		return err
	}
	lockHash := sha256.Sum256(encoded)
	entries = append(entries, pendingEntry{Path: "rasql.lock.json", Old: oldLock, Desired: fileState{State: "present", SHA256: hex.EncodeToString(lockHash[:])}})
	if err := writePending(root, entries); err != nil {
		return err
	}
	if err := plan.Commit(); err != nil {
		return err
	}
	if err := os.WriteFile(lockPath, encoded, 0o600); err != nil {
		return err
	}
	if err := removePending(root); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "updated schema and generated %s\n", cfg.Output)
	return nil
}

func resultName(query compilerir.GoQuery) string {
	if query.Result == nil {
		return ""
	}
	return query.Result.Name
}
func queryInputs(queries []compilerir.QueryAnalysis) []compilerlock.QueryDigestInput {
	out := make([]compilerlock.QueryDigestInput, 0, len(queries))
	for _, q := range queries {
		out = append(out, compilerlock.QueryDigestInput{ID: string(q.ID), SQL: compilerlock.SourceFile{Path: q.SQLPath, SHA256: q.SQLSHA256}, Operation: q.Operation, Cardinality: q.Cardinality})
	}
	return out
}

func (c command) runSchemaImport(args []string) error {
	return c.runSchemaUpdate(args)
}

func (c command) runSchemaVerify(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "schema verify")
	configPath := flags.String("config", "", "settings file")
	dsn := flags.String("dsn", "", "live connection string")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if cfg.Engine == nil || cfg.Schema == nil {
		return errors.New("schema verify: config requires engine and schema")
	}
	root, err := moduleRootForConfig(*configPath)
	if err != nil {
		return err
	}
	lockBytes, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
	if err != nil {
		return fmt.Errorf("schema verify: read lock: %w", err)
	}
	lock, err := compilerlock.Decode(lockBytes)
	if err != nil {
		return err
	}
	request := schemasource.Request{ModuleRoot: root, Engine: schemasource.EngineConfig{Dialect: cfg.Engine.Dialect, Profile: cfg.Engine.Profile}, Source: schemasource.SchemaSourceConfig{Kind: cfg.Schema.Kind, Identity: cfg.Schema.Identity, Paths: cfg.Schema.Paths, Inputs: cfg.Schema.Inputs, Command: cfg.Schema.Command, Environment: cfg.Schema.Environment}, LiveDSN: *dsn, BootstrapDSN: *dsn, TempRoot: filepath.Join(root, ".tmp")}
	verified, err := schemasource.Verify(context.Background(), request, schemasource.DefaultDependencies(), lock)
	if err != nil {
		return fmt.Errorf("schema verify: %w", err)
	}
	if len(verified.Differences) != 0 {
		return fmt.Errorf("schema verify: schema drift: %v", verified.Differences)
	}
	_, _ = fmt.Fprintln(c.output, "schema is verified")
	return nil
}

func moduleRootForConfig(path string) (string, error) {
	if path != "" {
		return filepath.Abs(filepath.Dir(path))
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Abs(wd)
}

func hasErrors(diagnostics []compilerir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == compilerir.DiagnosticError {
			return true
		}
	}
	return false
}
