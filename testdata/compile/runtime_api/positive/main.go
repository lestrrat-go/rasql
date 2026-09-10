package runtimefixture

import (
	"context"
	"database/sql"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
)

type executor struct{}

func (executor) Dialect() dialect.Dialect                                        { return dialect.SQLite() }
func (executor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) { return nil, nil }
func (executor) Exec(context.Context, stmt.Statement) (sql.Result, error)        { return nil, nil }

var _ rasql.Executor = executor{}
var _ rasql.CodecProvider = codecExecutor{}

type codecExecutor struct{ executor }

func (codecExecutor) Codecs() rasql.CodecRegistry { return nil }

func use(rasql.DB, rasql.EngineProfile, rasql.Executor, rasql.CodecRegistry) {
	var _ = rasql.AsExecutor
	var _ = rasql.WithEngineProfile
	var _ = rasql.WithCodecs
	var _ = rasql.Rows[int]
	var tx *sql.Tx
	db, _ := rasql.New(tx, dialect.SQLite())
	_, _ = rasql.AsExecutor(db, rasql.EngineProfile{})
}
