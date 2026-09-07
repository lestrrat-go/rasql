package querydescribe

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
)

type PostgreSQLDescriber struct{}

func NewPostgreSQL() compilerquery.Describer { return PostgreSQLDescriber{} }

func (PostgreSQLDescriber) Describe(ctx context.Context, request compilerquery.DescribeRequest) (compilerquery.Description, error) {
	if request.DSN == "" {
		return compilerquery.Description{}, fmt.Errorf("querydescribe: PostgreSQL DSN is required")
	}
	conn, err := pgx.Connect(ctx, request.DSN)
	if err != nil {
		return compilerquery.Description{}, err
	}
	defer conn.Close(ctx)
	name := "rasql_describe_" + strings.ReplaceAll(request.Name, "-", "_")
	description, err := conn.Prepare(ctx, name, request.SQL)
	if err != nil {
		return compilerquery.Description{}, err
	}
	defer func() { _ = conn.Deallocate(ctx, name) }()
	result := compilerquery.Description{Parameters: make([]compilerquery.ValueEvidence, len(description.ParamOIDs)), Results: make([]compilerquery.ValueEvidence, len(description.Fields))}
	for i, oid := range description.ParamOIDs {
		kind := ""
		if typ, ok := conn.TypeMap().TypeForOID(oid); ok {
			kind = pgLogicalKind(typ.Name)
		}
		result.Parameters[i] = compilerquery.ValueEvidence{Name: parameterName(request.ParameterNames, i), Type: compilerquery.TypeEvidence{LogicalKind: kind, Certainty: certainty(kind)}}
	}
	for i, field := range description.Fields {
		kind := ""
		if typ, ok := conn.TypeMap().TypeForOID(field.DataTypeOID); ok {
			kind = pgLogicalKind(typ.Name)
		}
		result.Results[i] = compilerquery.ValueEvidence{Name: field.Name, Type: compilerquery.TypeEvidence{LogicalKind: kind, Certainty: certainty(kind)}}
	}
	return result, nil
}

func parameterName(names []string, index int) string {
	if index < len(names) {
		return names[index]
	}
	return fmt.Sprintf("$%d", index+1)
}
func certainty(kind string) compilerir.Certainty {
	if kind == "" {
		return compilerir.CertaintyUnknown
	}
	return compilerir.CertaintyKnown
}
func pgLogicalKind(name string) string {
	switch strings.ToLower(name) {
	case "bool":
		return "bool"
	case "int2", "int4", "int8":
		return "integer"
	case "float4", "float8":
		return "float"
	case "text", "varchar", "bpchar":
		return "text"
	case "bytea":
		return "bytes"
	case "timestamp", "timestamptz", "date", "time":
		return "time"
	case "json", "jsonb":
		return "json"
	case "uuid":
		return "uuid"
	case "numeric", "decimal":
		return "decimal"
	default:
		return ""
	}
}
