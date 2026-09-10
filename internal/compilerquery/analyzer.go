package compilerquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/queryevidence"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/internal/sourcefile"
	"github.com/lestrrat-go/rasql/namedsql"
)

type DescribeRequest = queryevidence.DescribeRequest
type TypeEvidence = queryevidence.TypeEvidence
type ValueEvidence = queryevidence.ValueEvidence
type Description = queryevidence.Description
type Describer = queryevidence.Describer
type Describers = queryevidence.Describers

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
		if request.Profile.Engine != engineprofile.PostgreSQL {
			for _, value := range append(append([]ValueDeclaration(nil), query.Parameters...), query.Results...) {
				if value.Scalar == "" {
					return schemasource.AnalysisResult{}, fmt.Errorf("query %q value %q requires a scalar declaration", query.ID, value.Name)
				}
			}
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
		var parameters, results []compilerir.SemanticValue
		if description.DeclaredOnly {
			if len(description.Parameters) != 0 || len(description.Results) != 0 {
				return schemasource.AnalysisResult{}, fmt.Errorf("query %q: declared-only description includes observations", query.ID)
			}
			parameters, err = declaredValues(query.Parameters, a.config.Mappings)
			if err != nil {
				return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameters: %w", query.ID, err)
			}
			results, err = declaredValues(query.Results, a.config.Mappings)
			if err != nil {
				return schemasource.AnalysisResult{}, fmt.Errorf("query %q results: %w", query.ID, err)
			}
		} else {
			parameters, err = mergeValues(query.Parameters, description.Parameters, parameterNames, a.config.Mappings)
			if err != nil {
				return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameters: %w", query.ID, err)
			}
			results, err = mergeValues(query.Results, description.Results, nil, a.config.Mappings)
			if err != nil {
				return schemasource.AnalysisResult{}, fmt.Errorf("query %q results: %w", query.ID, err)
			}
		}
		if request.Profile.Engine == engineprofile.PostgreSQL {
			for i, value := range query.Parameters {
				if value.Scalar == "" && (i >= len(parameters) || parameters[i].LogicalKind == "" || parameters[i].TypeCertainty == compilerir.CertaintyUnknown) {
					return schemasource.AnalysisResult{}, fmt.Errorf("query %q parameter %q has unresolved type", query.ID, value.Name)
				}
			}
			for i, value := range query.Results {
				if value.Scalar == "" && (i >= len(results) || results[i].LogicalKind == "" || results[i].TypeCertainty == compilerir.CertaintyUnknown) {
					return schemasource.AnalysisResult{}, fmt.Errorf("query %q result %q has unresolved type", query.ID, value.Name)
				}
			}
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

func mergeValues(declarations []ValueDeclaration, observed []ValueEvidence, names []string, mappings compilerir.MappingConfig) ([]compilerir.SemanticValue, error) {
	if len(names) != 0 {
		if len(observed) != len(names) {
			return nil, fmt.Errorf("observed occurrence count %d differs from lowered count %d", len(observed), len(names))
		}
		collapsed := make([]ValueEvidence, 0, len(declarations))
		seen := map[string]struct{}{}
		for i, name := range names {
			if i >= len(observed) {
				break
			}
			fact := observed[i]
			fact.Name = name
			if _, ok := seen[name]; ok {
				for _, prior := range collapsed {
					if prior.Name == name && (!sameTypeEvidence(prior.Type, fact.Type) || !sameNullable(prior.Nullable, fact.Nullable)) {
						return nil, fmt.Errorf("repeated parameter %q has conflicting evidence", name)
					}
				}
				continue
			}
			seen[name] = struct{}{}
			collapsed = append(collapsed, fact)
		}
		observed = collapsed
	}
	if len(declarations) != len(observed) {
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
		mappedScalar, mapped := compilerir.ResolveQueryScalar(fact.Type.LogicalKind, fact.Type.Native, fact.Type.Integer, mappings)
		if declaration.Scalar != "" && mapped && declaration.Scalar != mappedScalar {
			return nil, fmt.Errorf("value %q type %q conflicts with %q", declaration.Name, mappedScalar, declaration.Scalar)
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
			scalar = mappedScalar
			if scalar == "" {
				scalar = fact.Type.LogicalKind
			}
		}
		if scalar == "" {
			return nil, fmt.Errorf("value %q has no scalar", declaration.Name)
		}
		typeCertainty := fact.Type.Certainty
		if typeCertainty == compilerir.CertaintyUnknown && scalar != "" {
			typeCertainty = compilerir.CertaintyDeclared
		}
		nullabilityCertainty := compilerir.CertaintyDeclared
		if fact.Nullable != nil {
			nullabilityCertainty = compilerir.CertaintyKnown
		}
		values[i] = compilerir.SemanticValue{Name: declaration.Name, Scalar: scalar, Nullable: nullable, TypeCertainty: typeCertainty, NullabilityCertainty: nullabilityCertainty, LogicalKind: fact.Type.LogicalKind, Native: cloneNative(fact.Type.Native), Integer: cloneInteger(fact.Type.Integer)}
	}
	return values, nil
}

func sameTypeEvidence(a, b TypeEvidence) bool {
	return a.LogicalKind == b.LogicalKind && a.Certainty == b.Certainty && reflect.DeepEqual(a.Native, b.Native) && reflect.DeepEqual(a.Integer, b.Integer)
}

func sameNullable(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func cloneNative(in *compilerir.NativeType) *compilerir.NativeType {
	if in == nil {
		return nil
	}
	out := *in
	out.Arguments = append([]string(nil), in.Arguments...)
	out.Element = cloneNative(in.Element)
	return &out
}
func cloneInteger(in *compilerir.IntegerTypeFacts) *compilerir.IntegerTypeFacts {
	if in == nil {
		return nil
	}
	out := *in
	return &out
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
