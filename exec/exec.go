// Package exec runs rendered statements against a database and reports what
// happened.
//
// DB pairs a database/sql handle with a dialect. New builds one, Begin returns
// another bound to a transaction, and hooks registered on either observe every
// statement that runs through it. The root rasql package re-exports every name
// here, so an application that wants the typed API needs only that one import;
// rasql/dynamic imports this package directly, because it needs the runtime
// without the typed facade.
package exec

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/nilcheck"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/stmt"
)

// Write renders and executes a write statement.
// It rejects a statement carrying a RETURNING clause, because ExecContext
// discards result rows; RenderWrite reads them instead.
func Write(ctx context.Context, db DB, s query.WriteStatement) (sql.Result, error) {
	if err := db.valid(); err != nil {
		return nil, err
	}
	if !nilcheck.Is(s) && len(s.Returning()) > 0 {
		return nil, fmt.Errorf("rasql: write statement has a RETURNING clause: use QueryWrite to read its rows")
	}
	rendered, err := render.Write(db.Dialect(), s)
	if err != nil {
		return nil, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return db.ExecRendered(ctx, rendered)
}

// RenderWrite renders a write statement that must carry a RETURNING clause,
// and reports an error when it does not.
func RenderWrite(db DB, s query.WriteStatement) (stmt.Statement, error) {
	if err := db.valid(); err != nil {
		return stmt.Statement{}, err
	}
	if nilcheck.Is(s) || len(s.Returning()) == 0 {
		return stmt.Statement{}, fmt.Errorf("rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
	}
	rendered, err := render.Write(db.Dialect(), s)
	if err != nil {
		return stmt.Statement{}, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return rendered, nil
}
