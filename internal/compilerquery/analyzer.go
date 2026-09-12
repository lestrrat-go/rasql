package compilerquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/namedsql"
)

type analyzer struct {
	config Config
}

func NewAnalyzer(config Config) (schemasource.Analyzer, error) {
	if config.ModuleRoot == "" {
		return nil, fmt.Errorf("compilerquery: module root is required")
	}
	return analyzer{config: cloneConfig(config)}, nil
}

func (a analyzer) Analyze(ctx context.Context, request schemasource.AnalysisRequest) (schemasource.AnalysisResult, error) {
	engine := compilerir.EngineIdentity{Dialect: engineName(request.Profile.Engine), Version: request.Profile.ID, Profile: request.Profile.ID}
	if err := ValidateConfig(a.config, engine); err != nil {
		return schemasource.AnalysisResult{}, err
	}
	queries := make([]compilerir.QueryAnalysis, 0, len(a.config.Queries))
	snapshots := make([]sourcefile.SourceFileSnapshot, 0, len(a.config.Queries))
	for _, query := range a.config.Queries {
		snapshot, err := sourcefile.SnapshotSourceFile(a.config.ModuleRoot, query.Input)
		if err != nil {
			return schemasource.AnalysisResult{}, err
		}
		snapshots = append(snapshots, snapshot)
		sqlText := string(snapshot.Bytes())
		template, err := namedsql.Parse(string(query.ID), sqlText)
		if err != nil {
			return schemasource.AnalysisResult{}, err
		}
		compiled, err := template.Compile(engineDialect(request.Profile.Engine))
		if err != nil {
			return schemasource.AnalysisResult{}, err
		}
		loweredSQL := compiled.SQL()
		definition := compiled.QueryDef()
		parameterNames := make([]string, len(definition.Parameters))
		copy(parameterNames, definition.Parameters)
		classification, err := ClassifySQL(loweredSQL)
		if err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q: %w", query.ID, err)
		}
		if query.Operation != classification.Operation {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q: declared operation %q differs from %q", query.ID, query.Operation, classification.Operation)
		}
		if err := validateCardinality(query, classification, request.Profile); err != nil {
			return schemasource.AnalysisResult{}, err
		}
		for _, value := range append(append([]ValueDeclaration(nil), query.Parameters...), query.Results...) {
			if value.Scalar == "" {
				return schemasource.AnalysisResult{}, fmt.Errorf("query %q value %q requires a scalar declaration", query.ID, value.Name)
			}
		}
		if err := checkSQLPrepares(ctx, request.DB, loweredSQL); err != nil {
			return schemasource.AnalysisResult{}, err
		}
		if err := compareParameterNames(query.Parameters, parameterNames); err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameters: %w", query.ID, err)
		}
		parameters, err := declaredValues(query.Parameters, a.config.Mappings)
		if err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameters: %w", query.ID, err)
		}
		results, err := declaredValues(query.Results, a.config.Mappings)
		if err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q results: %w", query.ID, err)
		}
		digest := sha256.Sum256(snapshot.Bytes())
		queries = append(queries, compilerir.QueryAnalysis{ID: query.ID, Name: query.Function, SQLPath: snapshot.Path(), SQLSHA256: hex.EncodeToString(digest[:]), Engine: engine, Operation: classification.Operation, Parameters: parameters, Results: results, Cardinality: query.Cardinality})
	}
	return schemasource.AnalysisResult{Queries: queries, Snapshots: snapshots}, nil
}

func declaredValues(declarations []ValueDeclaration, mappings compilerir.MappingConfig) ([]compilerir.SemanticValue, error) {
	values := make([]compilerir.SemanticValue, len(declarations))
	for i, declaration := range declarations {
		if declaration.Name == "" || declaration.Scalar == "" || declaration.Nullable == nil {
			return nil, fmt.Errorf("value %q requires name, scalar, and nullable declaration", declaration.Name)
		}
		logicalKind, ok, err := compilerir.DeclaredQueryLogicalKind(declaration.Scalar, mappings)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("value %q scalar %q has no declared logical kind", declaration.Name, declaration.Scalar)
		}
		values[i] = compilerir.SemanticValue{Name: declaration.Name, Scalar: declaration.Scalar, Nullable: *declaration.Nullable, TypeCertainty: compilerir.CertaintyDeclared, NullabilityCertainty: compilerir.CertaintyDeclared, LogicalKind: logicalKind}
	}
	return values, nil
}

func Analyze(ctx context.Context, request schemasource.AnalysisRequest, config Config) (schemasource.AnalysisResult, error) {
	a, err := NewAnalyzer(config)
	if err != nil {
		return schemasource.AnalysisResult{}, err
	}
	return a.Analyze(ctx, request)
}

func engineName(engine engineprofile.EngineID) string {
	switch engine {
	case engineprofile.PostgreSQL:
		return "postgresql"
	case engineprofile.MySQL:
		return "mysql"
	case engineprofile.SQLite:
		return "sqlite"
	}
	return ""
}
func engineDialect(engine engineprofile.EngineID) dialect.Dialect {
	switch engine {
	case engineprofile.PostgreSQL:
		return dialect.PostgreSQL()
	case engineprofile.MySQL:
		return dialect.MySQL()
	default:
		return dialect.SQLite()
	}
}

func validateCardinality(q QueryConfig, c Classification, p engineprofile.Profile) error {
	if c.Operation == "select" && q.Cardinality != "one" && q.Cardinality != "maybe" && q.Cardinality != "many" {
		return fmt.Errorf("query %q: SELECT requires one, maybe, or many cardinality", q.ID)
	}
	if c.Operation != "select" && q.Cardinality == "exec" && len(q.Results) != 0 {
		return fmt.Errorf("query %q: exec cannot declare results", q.ID)
	}
	if c.Operation != "select" && q.Cardinality != "exec" && !c.Returning {
		return fmt.Errorf("query %q: DML without RETURNING requires exec", q.ID)
	}
	if c.Operation != "select" && c.Returning && q.Cardinality != "one" && q.Cardinality != "maybe" && q.Cardinality != "many" {
		return fmt.Errorf("query %q: returning DML requires one, maybe, or many cardinality", q.ID)
	}
	if c.Operation != "select" && c.Returning && len(q.Results) == 0 {
		return fmt.Errorf("query %q: returning DML requires results", q.ID)
	}
	if c.Returning && p.Capabilities.Returning == engineprofile.ReturningNone {
		return fmt.Errorf("query %q: returning is unsupported by profile", q.ID)
	}
	if c.Returning && p.Engine == engineprofile.MySQL {
		return fmt.Errorf("query %q: MySQL does not support returning", q.ID)
	}
	return nil
}

func compareParameterNames(declarations []ValueDeclaration, names []string) error {
	unique := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	if len(declarations) != len(unique) {
		return fmt.Errorf("declared %d parameters, SQL uses %d distinct parameters", len(declarations), len(unique))
	}
	for i := range unique {
		if declarations[i].Name != unique[i] {
			return fmt.Errorf("parameter %d is %q, declaration is %q", i, unique[i], declarations[i].Name)
		}
	}
	return nil
}

func cloneConfig(in Config) Config {
	out := in
	out.Mappings = in.Mappings.Clone()
	out.Queries = append([]QueryConfig(nil), in.Queries...)
	for i := range out.Queries {
		out.Queries[i].Parameters = append([]ValueDeclaration(nil), in.Queries[i].Parameters...)
		out.Queries[i].Results = append([]ValueDeclaration(nil), in.Queries[i].Results...)
	}
	return out
}
