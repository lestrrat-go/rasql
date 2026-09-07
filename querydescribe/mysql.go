package querydescribe

import (
	"context"
	"database/sql"

	"github.com/lestrrat-go/rasql/internal/compilerquery"
)

type MySQLDescriber struct{ DB *sql.DB }

func (d MySQLDescriber) Describe(ctx context.Context, request compilerquery.DescribeRequest) (compilerquery.Description, error) {
	return NewDeclared(d.DB).Describe(ctx, request)
}
