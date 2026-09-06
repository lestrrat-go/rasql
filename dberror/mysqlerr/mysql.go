// Package mysqlerr classifies errors from github.com/go-sql-driver/mysql.
package mysqlerr

import (
	"errors"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/dberror"
)

type classifier struct{}

// New returns a classifier for go-sql-driver/mysql errors.
func New() dberror.Classifier { return classifier{} }

func (classifier) Classify(err error) (dberror.Metadata, bool) {
	var native *mysql.MySQLError
	if !errors.As(err, &native) {
		return dberror.Metadata{}, false
	}
	category, ok := categories[native.Number]
	if !ok {
		return dberror.Metadata{}, false
	}
	return dberror.Metadata{
		Category:   category,
		NativeCode: strconv.FormatUint(uint64(native.Number), 10),
		SQLState:   strings.TrimRight(string(native.SQLState[:]), "\x00"),
	}, true
}

var categories = map[uint16]dberror.Category{
	1062: dberror.UniqueViolation,
	1451: dberror.ForeignKeyViolation,
	1452: dberror.ForeignKeyViolation,
	1048: dberror.NotNullViolation,
	3819: dberror.CheckViolation,
	1205: dberror.TransactionConflict,
	1213: dberror.TransactionConflict,
}
