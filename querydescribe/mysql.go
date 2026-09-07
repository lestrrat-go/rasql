package querydescribe

import (
	"context"
	"database/sql"

	"github.com/lestrrat-go/rasql/internal/queryevidence"
)

type MySQLDescriber struct{ DB *sql.DB }

func (d MySQLDescriber) Describe(ctx context.Context, request queryevidence.DescribeRequest) (queryevidence.Description, error) {
	return NewDeclared(d.DB).Describe(ctx, request)
}
