package querydescribe

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/compilerquery"
)

type declaredDescriber struct{ db *sql.DB }

func NewDeclared(db *sql.DB) compilerquery.Describer { return declaredDescriber{db: db} }
func NewMySQL(db *sql.DB) compilerquery.Describer    { return declaredDescriber{db: db} }

func (d declaredDescriber) Describe(ctx context.Context, request compilerquery.DescribeRequest) (compilerquery.Description, error) {
	if d.db == nil {
		return compilerquery.Description{}, fmt.Errorf("querydescribe: nil database")
	}
	stmt, err := d.db.PrepareContext(ctx, request.SQL)
	if err != nil {
		return compilerquery.Description{}, err
	}
	if err := stmt.Close(); err != nil {
		return compilerquery.Description{}, err
	}
	return compilerquery.Description{}, nil
}
