// Package pgerr classifies errors from github.com/jackc/pgx/v5.
package pgerr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lestrrat-go/rasql/dberror"
)

type classifier struct{}

// New returns a classifier for pgx PostgreSQL errors.
func New() dberror.Classifier { return classifier{} }

func (classifier) Classify(err error) (dberror.Metadata, bool) {
	var native *pgconn.PgError
	if !errors.As(err, &native) {
		return dberror.Metadata{}, false
	}
	category, ok := categories[native.Code]
	if !ok {
		return dberror.Metadata{}, false
	}
	return dberror.Metadata{
		Category:   category,
		SQLState:   native.Code,
		NativeCode: native.Code,
		Constraint: native.ConstraintName,
		Table:      native.TableName,
		Column:     native.ColumnName,
	}, true
}

var categories = map[string]dberror.Category{
	"23505": dberror.UniqueViolation,
	"23503": dberror.ForeignKeyViolation,
	"23502": dberror.NotNullViolation,
	"23514": dberror.CheckViolation,
	"40001": dberror.TransactionConflict,
	"40P01": dberror.TransactionConflict,
	"55P03": dberror.TransactionConflict,
}
