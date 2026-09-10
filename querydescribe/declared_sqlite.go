package querydescribe

import (
	"database/sql"

	"github.com/lestrrat-go/rasql/internal/queryevidence"
)

func NewSQLitePrepare(db *sql.DB) queryevidence.Describer  { return NewDeclared(db) }
func NewSQLiteDeclared(db *sql.DB) queryevidence.Describer { return NewDeclared(db) }
