package queryevidence

import (
	"context"
	"database/sql"

	"github.com/lestrrat-go/rasql/internal/compilerir"
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
