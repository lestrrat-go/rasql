package compilerquery

import (
	"context"
	"database/sql"
	"fmt"
)

// checkSQLPrepares asks the engine to parse and plan loweredSQL against the live, migrated
// schema and throws the prepared statement away. It is the only step in analysis that puts a
// hand-written query in front of the server -- ClassifySQL is a token scan, and the generated Go
// is built from the declared scalars alone -- so a misspelled column or table, a syntax error, or
// a placeholder count the engine refuses fails generation here instead of at first execution.
//
// The engine's own error is returned unwrapped, because it already names the query's problem
// better than any wrapper around it: rasqlgen prints it as "generate: <engine message>".
func checkSQLPrepares(ctx context.Context, db *sql.DB, loweredSQL string) error {
	if db == nil {
		return fmt.Errorf("compilerquery: database connection is required")
	}
	stmt, err := db.PrepareContext(ctx, loweredSQL)
	if err != nil {
		return err
	}
	return stmt.Close()
}
