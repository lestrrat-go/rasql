package queryevidence_test

import (
	"context"

	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/queryevidence"
)

type spy struct{}

func (spy) Describe(context.Context, queryevidence.DescribeRequest) (queryevidence.Description, error) {
	return queryevidence.Description{}, nil
}

var _ queryevidence.Describer = spy{}
var _ compilerquery.Describer = spy{}
var _ queryevidence.Describer = compilerquery.Describer(nil)
