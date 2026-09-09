package querydescribe

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/queryevidence"
)

type fakePGConn struct {
	prepareErr, deallocateErr, closeErr error
	description                         *pgconn.StatementDescription
	prepares, deallocates, closes       int
	typemap                             *pgtype.Map
	cleanupCanceled                     bool
}

func (f *fakePGConn) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	f.prepares++
	return f.description, f.prepareErr
}
func (f *fakePGConn) Deallocate(ctx context.Context, _ string) error {
	f.deallocates++
	if ctx.Err() != nil {
		f.cleanupCanceled = true
	}
	return f.deallocateErr
}
func (f *fakePGConn) Close(ctx context.Context) error {
	f.closes++
	if ctx.Err() != nil {
		f.cleanupCanceled = true
	}
	return f.closeErr
}
func (f *fakePGConn) TypeMap() *pgtype.Map {
	if f.typemap != nil {
		return f.typemap
	}
	return pgtype.NewMap()
}

func TestPostgreSQLDescribeCleanupLifecycle(t *testing.T) {
	tests := []struct {
		name                                   string
		prepare, deallocate, close             error
		wantPrepare, wantDeallocate, wantClose int
	}{
		{name: "connect", prepare: errors.New("prepare"), wantPrepare: 1, wantClose: 1},
		{name: "success", wantPrepare: 1, wantDeallocate: 1, wantClose: 1},
		{name: "deallocate", deallocate: errors.New("deallocate"), wantPrepare: 1, wantDeallocate: 1, wantClose: 1},
		{name: "close", close: errors.New("close"), wantPrepare: 1, wantDeallocate: 1, wantClose: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := &fakePGConn{prepareErr: test.prepare, deallocateErr: test.deallocate, closeErr: test.close, description: &pgconn.StatementDescription{}}
			d := PostgreSQLDescriber{connector: func(context.Context, string) (postgresDescribeConn, error) { return conn, nil }}
			_, err := d.Describe(t.Context(), queryevidence.DescribeRequest{DSN: "owned", Name: "q", SQL: "SELECT 1"})
			if test.prepare != nil && err == nil {
				t.Fatal("expected prepare error")
			}
			if conn.prepares != test.wantPrepare || conn.deallocates != test.wantDeallocate || conn.closes != test.wantClose {
				t.Fatalf("calls=%d/%d/%d", conn.prepares, conn.deallocates, conn.closes)
			}
		})
	}
}

func TestPostgreSQLDescribeRejectsBlankDSNBeforeConnect(t *testing.T) {
	d := PostgreSQLDescriber{connector: func(context.Context, string) (postgresDescribeConn, error) { t.Fatal("connected"); return nil, nil }}
	if _, err := d.Describe(t.Context(), queryevidence.DescribeRequest{}); err == nil {
		t.Fatal("expected blank DSN error")
	}
}

func TestPostgreSQLDescribeConnectFailure(t *testing.T) {
	d := PostgreSQLDescriber{connector: func(context.Context, string) (postgresDescribeConn, error) { return nil, errors.New("connect") }}
	if _, err := d.Describe(t.Context(), queryevidence.DescribeRequest{DSN: "owned"}); err == nil {
		t.Fatal("expected connect error")
	}
}

func TestPostgreSQLDescribeMapsKnownAndUnknownOID(t *testing.T) {
	typemap := pgtype.NewMap()
	typemap.RegisterType(&pgtype.Type{Name: "int4", OID: 23, Codec: pgtype.Int4Codec{}})
	typemap.RegisterType(&pgtype.Type{Name: "bool", OID: 16, Codec: pgtype.BoolCodec{}})
	conn := &fakePGConn{typemap: typemap, description: &pgconn.StatementDescription{ParamOIDs: []uint32{23, 16, 999999}, Fields: []pgconn.FieldDescription{{Name: "flag", DataTypeOID: 16}, {Name: "value", DataTypeOID: 23}}}}
	d := PostgreSQLDescriber{connector: func(context.Context, string) (postgresDescribeConn, error) { return conn, nil }}
	got, err := d.Describe(t.Context(), queryevidence.DescribeRequest{DSN: "owned", Name: "q", SQL: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	wantBoolNative := &compilerir.NativeType{Dialect: "postgresql", Name: "bool", Kind: "builtin"}
	parameterBool := got.Parameters[1].Type
	resultBool := got.Results[0].Type
	if parameterBool.LogicalKind != "boolean" || parameterBool.Certainty != compilerir.CertaintyKnown || !reflect.DeepEqual(parameterBool.Native, wantBoolNative) {
		t.Fatalf("boolean parameter evidence=%#v", parameterBool)
	}
	if resultBool.LogicalKind != "boolean" || resultBool.Certainty != compilerir.CertaintyKnown || !reflect.DeepEqual(resultBool.Native, wantBoolNative) {
		t.Fatalf("boolean result evidence=%#v", resultBool)
	}
	if got.Parameters[0].Type.LogicalKind != "integer" || got.Parameters[2].Type.Certainty != compilerir.CertaintyUnknown || got.Results[1].Type.LogicalKind != "integer" {
		t.Fatalf("description=%#v", got)
	}
}

func TestPostgreSQLDescribeCleanupIgnoresCanceledCaller(t *testing.T) {
	conn := &fakePGConn{description: &pgconn.StatementDescription{}}
	d := PostgreSQLDescriber{connector: func(context.Context, string) (postgresDescribeConn, error) { return conn, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = d.Describe(ctx, queryevidence.DescribeRequest{DSN: "owned", Name: "q", SQL: "SELECT 1"})
	if conn.cleanupCanceled {
		t.Fatal("cleanup used canceled caller context")
	}
}
