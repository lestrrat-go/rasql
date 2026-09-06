// Package sqliteerr classifies errors from modernc.org/sqlite.
package sqliteerr

import (
	"errors"
	"strconv"

	"github.com/lestrrat-go/rasql/dberror"
	"modernc.org/sqlite"
)

type classifier struct{}

// New returns a classifier for modernc.org/sqlite errors.
func New() dberror.Classifier { return classifier{} }

func (classifier) Classify(err error) (dberror.Metadata, bool) {
	var native *sqlite.Error
	if !errors.As(err, &native) {
		return dberror.Metadata{}, false
	}
	category, ok := categories[native.Code()]
	if !ok {
		return dberror.Metadata{}, false
	}
	return dberror.Metadata{Category: category, NativeCode: strconv.Itoa(native.Code())}, true
}

var categories = map[int]dberror.Category{
	1555: dberror.UniqueViolation,
	2067: dberror.UniqueViolation,
	787:  dberror.ForeignKeyViolation,
	1299: dberror.NotNullViolation,
	275:  dberror.CheckViolation,
	5:    dberror.TransactionConflict,
	6:    dberror.TransactionConflict,
	517:  dberror.TransactionConflict,
}
