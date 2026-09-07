package querydescribe

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/queryevidence"
)

type declaredDescriber struct{}

func NewDeclared(_ *sql.DB) queryevidence.Describer { return declaredDescriber{} }
func NewMySQL(_ *sql.DB) queryevidence.Describer    { return declaredDescriber{} }

func (d declaredDescriber) Describe(ctx context.Context, request queryevidence.DescribeRequest) (queryevidence.Description, error) {
	if request.DB == nil {
		return queryevidence.Description{}, fmt.Errorf("querydescribe: nil database")
	}
	stmt, err := request.DB.PrepareContext(ctx, request.SQL)
	if err != nil {
		return queryevidence.Description{}, err
	}
	if err := stmt.Close(); err != nil {
		return queryevidence.Description{}, err
	}
	return queryevidence.Description{DeclaredOnly: true}, nil
}
