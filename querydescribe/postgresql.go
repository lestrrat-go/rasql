package querydescribe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/queryevidence"
)

type postgresDescribeConn interface {
	Prepare(context.Context, string, string) (*pgconn.StatementDescription, error)
	Deallocate(context.Context, string) error
	Close(context.Context) error
	TypeMap() *pgtype.Map
}
type postgresDescribeConnector func(context.Context, string) (postgresDescribeConn, error)
type PostgreSQLDescriber struct{ connector postgresDescribeConnector }

func NewPostgreSQL() queryevidence.Describer {
	return PostgreSQLDescriber{connector: func(ctx context.Context, dsn string) (postgresDescribeConn, error) { return pgx.Connect(ctx, dsn) }}
}

func (d PostgreSQLDescriber) Describe(ctx context.Context, request queryevidence.DescribeRequest) (queryevidence.Description, error) {
	if strings.TrimSpace(request.DSN) == "" {
		return queryevidence.Description{}, fmt.Errorf("querydescribe: PostgreSQL DSN is required")
	}
	if d.connector == nil {
		d.connector = func(ctx context.Context, dsn string) (postgresDescribeConn, error) { return pgx.Connect(ctx, dsn) }
	}
	return d.describe(ctx, request)
}

func (d PostgreSQLDescriber) describe(ctx context.Context, request queryevidence.DescribeRequest) (queryevidence.Description, error) {
	conn, err := d.connector(ctx, request.DSN)
	if err != nil {
		return queryevidence.Description{}, redactPGError(err, request.DSN)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	name := "rasql_describe_" + strings.ReplaceAll(request.Name, "-", "_")
	description, err := conn.Prepare(ctx, name, request.SQL)
	if err != nil {
		return queryevidence.Description{}, errors.Join(err, conn.Close(cleanupCtx))
	}
	result := queryevidence.Description{Parameters: make([]queryevidence.ValueEvidence, len(description.ParamOIDs)), Results: make([]queryevidence.ValueEvidence, len(description.Fields))}
	for i, oid := range description.ParamOIDs {
		result.Parameters[i] = queryevidence.ValueEvidence{Name: parameterName(request.ParameterNames, i), Type: pgTypeEvidence(conn.TypeMap(), oid)}
	}
	for i, field := range description.Fields {
		result.Results[i] = queryevidence.ValueEvidence{Name: field.Name, Type: pgTypeEvidence(conn.TypeMap(), field.DataTypeOID)}
	}
	return result, errors.Join(conn.Deallocate(cleanupCtx, name), conn.Close(cleanupCtx))
}

type redactedPGError struct {
	message string
	cause   error
}

func (e *redactedPGError) Error() string { return e.message }
func (e *redactedPGError) Unwrap() error { return e.cause }
func redactPGError(err error, dsn string) error {
	if dsn == "" {
		return err
	}
	return &redactedPGError{message: strings.ReplaceAll(err.Error(), dsn, "[redacted]"), cause: err}
}

func pgTypeEvidence(m *pgtype.Map, oid uint32) queryevidence.TypeEvidence {
	typ, ok := m.TypeForOID(oid)
	if !ok {
		return queryevidence.TypeEvidence{Certainty: compilerir.CertaintyUnknown}
	}
	name := strings.ToLower(typ.Name)
	kind := pgLogicalKind(name)
	evidence := queryevidence.TypeEvidence{LogicalKind: kind, Native: &compilerir.NativeType{Dialect: "postgresql", Name: typ.Name, Kind: "builtin"}, Certainty: certainty(kind)}
	if kind == "integer" {
		evidence.Integer = &compilerir.IntegerTypeFacts{}
	}
	if kind == "" {
		evidence.Native.Kind = "other"
	}
	return evidence
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
		return "boolean"
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
