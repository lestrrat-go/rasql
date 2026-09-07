package querydescribe

import (
	"database/sql"

	"github.com/lestrrat-go/rasql/internal/compilerquery"
)

func NewSQLitePrepare(db *sql.DB) compilerquery.Describer  { return NewDeclared(db) }
func NewSQLiteDeclared(db *sql.DB) compilerquery.Describer { return NewDeclared(db) }
