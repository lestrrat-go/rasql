package compilerquery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
)

type DescribeRequest struct {
	Name           string
	Engine         compilerir.EngineIdentity
	Operation      string
	SQL            string
	ParameterNames []string
	DB             *sql.DB
	DSN            string
}

type TypeEvidence struct {
	LogicalKind string
	Native      *compilerir.NativeType
	Integer     *compilerir.IntegerTypeFacts
	Certainty   compilerir.Certainty
}

type ValueEvidence struct {
	Name     string
	Type     TypeEvidence
	Nullable *bool
}

type Description struct {
	Parameters []ValueEvidence
	Results    []ValueEvidence
}

type Describer interface {
	Describe(context.Context, DescribeRequest) (Description, error)
}

type Describers struct {
	PostgreSQL Describer
	MySQL      Describer
	SQLite     Describer
}

type analyzer struct {
	config     Config
	describers Describers
}

func NewAnalyzer(config Config, describers Describers) (schemasource.Analyzer, error) {
	if config.ModuleRoot == "" {
		return nil, fmt.Errorf("compilerquery: module root is required")
	}
	return analyzer{config: cloneConfig(config), describers: describers}, nil
}

func (a analyzer) Analyze(ctx context.Context, request schemasource.AnalysisRequest) (schemasource.AnalysisResult, error) {
	engine := compilerir.EngineIdentity{Dialect: engineName(request.Profile.Engine), Version: request.Profile.ID, Profile: request.Profile.ID}
	if err := ValidateConfig(a.config, engine); err != nil {
		return schemasource.AnalysisResult{}, err
	}
	queries := make([]compilerir.QueryAnalysis, 0, len(a.config.Queries))
	snapshots := make([]compilerlock.SourceFileSnapshot, 0, len(a.config.Queries))
	for _, query := range a.config.Queries {
		snapshot, err := compilerlock.SnapshotSourceFile(a.config.ModuleRoot, query.Input)
		if err != nil {
			return schemasource.AnalysisResult{}, err
		}
		snapshots = append(snapshots, snapshot)
		sqlText := string(snapshot.Bytes())
		loweredSQL, parameterNames, err := lowerNamedSQL(sqlText, engineDialect(request.Profile.Engine))
		if err != nil {
			return schemasource.AnalysisResult{}, err
		}
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
		describer := a.describer(request.Profile.Engine)
		if describer == nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q: no describer for %s", query.ID, engine.Dialect)
		}
		description, err := describer.Describe(ctx, DescribeRequest{Name: string(query.ID), Engine: engine, Operation: classification.Operation, SQL: loweredSQL, ParameterNames: parameterNames, DB: request.DB, DSN: request.DSN})
		if err != nil {
			return schemasource.AnalysisResult{}, err
		}
		if err := compareParameterNames(query.Parameters, parameterNames); err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameters: %w", query.ID, err)
		}
		parameters, err := mergeValues(query.Parameters, description.Parameters, parameterNames)
		if err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameters: %w", query.ID, err)
		}
		results, err := mergeValues(query.Results, description.Results, nil)
		if err != nil {
			return schemasource.AnalysisResult{}, fmt.Errorf("query %q results: %w", query.ID, err)
		}
		digest := sha256.Sum256(snapshot.Bytes())
		queries = append(queries, compilerir.QueryAnalysis{ID: query.ID, Name: query.Function, SQLPath: snapshot.Path(), SQLSHA256: hex.EncodeToString(digest[:]), Engine: engine, Operation: classification.Operation, Parameters: parameters, Results: results, Cardinality: query.Cardinality})
	}
	return schemasource.AnalysisResult{Queries: queries, Snapshots: snapshots}, nil
}

func Analyze(ctx context.Context, request schemasource.AnalysisRequest, config Config, describers Describers) (schemasource.AnalysisResult, error) {
	a, err := NewAnalyzer(config, describers)
	if err != nil {
		return schemasource.AnalysisResult{}, err
	}
	return a.Analyze(ctx, request)
}

func (a analyzer) describer(engine engineprofile.EngineID) Describer {
	switch engine {
	case engineprofile.PostgreSQL:
		return a.describers.PostgreSQL
	case engineprofile.MySQL:
		return a.describers.MySQL
	case engineprofile.SQLite:
		return a.describers.SQLite
	}
	return nil
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
	if c.Returning && p.Engine == engineprofile.MySQL {
		return fmt.Errorf("query %q: MySQL does not support returning", q.ID)
	}
	return nil
}

func mergeValues(declarations []ValueDeclaration, observed []ValueEvidence, names []string) ([]compilerir.SemanticValue, error) {
	if len(declarations) != len(observed) && len(observed) != 0 {
		return nil, fmt.Errorf("declaration count %d differs from observed count %d", len(declarations), len(observed))
	}
	if len(declarations) == 0 && len(observed) == 0 {
		return nil, nil
	}
	values := make([]compilerir.SemanticValue, len(declarations))
	for i, declaration := range declarations {
		fact := ValueEvidence{Name: declaration.Name, Type: TypeEvidence{Certainty: compilerir.CertaintyDeclared}}
		if i < len(observed) {
			fact = observed[i]
		}
		if declaration.Name != fact.Name && fact.Name != "" {
			return nil, fmt.Errorf("value %d name %q differs from %q", i, fact.Name, declaration.Name)
		}
		if declaration.Scalar != "" && fact.Type.LogicalKind != "" && declaration.Scalar != fact.Type.LogicalKind {
			return nil, fmt.Errorf("value %q type %q conflicts with %q", declaration.Name, fact.Type.LogicalKind, declaration.Scalar)
		}
		nullable := false
		if declaration.Nullable != nil {
			nullable = *declaration.Nullable
		}
		if fact.Nullable != nil && declaration.Nullable != nil && *fact.Nullable != *declaration.Nullable {
			return nil, fmt.Errorf("value %q nullability conflicts", declaration.Name)
		}
		scalar := declaration.Scalar
		if scalar == "" {
			scalar = fact.Type.LogicalKind
		}
		values[i] = compilerir.SemanticValue{Name: declaration.Name, Scalar: scalar, Nullable: nullable, TypeCertainty: fact.Type.Certainty, NullabilityCertainty: compilerir.CertaintyDeclared}
	}
	return values, nil
}

func compareParameterNames(declarations []ValueDeclaration, names []string) error {
	if len(declarations) != len(names) {
		return fmt.Errorf("declared %d parameters, SQL uses %d", len(declarations), len(names))
	}
	for i := range names {
		if declarations[i].Name != names[i] {
			return fmt.Errorf("parameter %d is %q, declaration is %q", i, names[i], declarations[i].Name)
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

func lowerNamedSQL(source string, d dialect.Dialect) (string, []string, error) {
	var out strings.Builder
	var names []string
	seen := map[string]struct{}{}
	for pos := 0; pos < len(source); {
		start := strings.Index(source[pos:], "{{")
		if start < 0 {
			out.WriteString(source[pos:])
			break
		}
		start += pos
		out.WriteString(source[pos:start])
		end := strings.Index(source[start+2:], "}}")
		if end < 0 {
			return "", nil, fmt.Errorf("compilerquery: unclosed bind action")
		}
		end += start + 2
		fields := strings.Fields(source[start+2 : end])
		if len(fields) < 2 || len(fields) > 3 || fields[0] != "bind" {
			return "", nil, fmt.Errorf("compilerquery: invalid bind action")
		}
		name, err := strconv.Unquote(fields[1])
		if err != nil || !validIdentifier(name) {
			return "", nil, fmt.Errorf("compilerquery: invalid bind name")
		}
		if len(fields) == 3 && !validColumnReference(fields[2]) {
			return "", nil, fmt.Errorf("compilerquery: invalid bind column reference")
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			names = append(names, name)
		}
		placeholder, err := d.Placeholder(len(names))
		if err != nil {
			return "", nil, err
		}
		out.WriteString(placeholder)
		pos = end + 2
	}
	return out.String(), names, nil
}

func validIdentifier(name string) bool {
	for i, r := range name {
		if i == 0 && !(r == '_' || unicode.IsLetter(r)) || i > 0 && !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return name != ""
}

func validColumnReference(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !validIdentifier(part) {
			return false
		}
	}
	return true
}
