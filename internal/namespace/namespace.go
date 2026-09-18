// Package namespace holds the one rule that a descriptor should not record the namespace the
// connection already resolves an unqualified name against, and should record every other
// namespace. internal/schemasource's generate path and the public catalog package's dump path
// both need this rule, so it lives here rather than in either of them: schemasource reads through
// an *sql.DB and dump reads through an *sql.Tx it already opened, and neither package can import
// the other.
package namespace

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
)

// EngineID maps a dialect name to the engine it names: "postgresql"/"postgres" to
// engineprofile.PostgreSQL, "mysql" to engineprofile.MySQL, and anything else, including
// "sqlite"/"sqlite3", to engineprofile.SQLite. A caller holding only a dialect name, rather than a
// full engineprofile.Profile, uses this to reach DefaultQuery and Default.
func EngineID(dialectName string) engineprofile.EngineID {
	switch strings.ToLower(dialectName) {
	case "postgresql", "postgres":
		return engineprofile.PostgreSQL
	case "mysql":
		return engineprofile.MySQL
	default:
		return engineprofile.SQLite
	}
}

// DefaultQuery reports the statement that asks a server which namespace it is connected to, and
// false for an engine this package has no such statement for.
//
// PostgreSQL answers with current_schema(), MySQL with DATABASE(), and SQLite with the name at
// sequence 0 of PRAGMA database_list, which is the database a CREATE TABLE carrying no qualifier
// writes into. Each statement returns exactly one row of one column.
func DefaultQuery(engine engineprofile.EngineID) (string, bool) {
	switch engine {
	case engineprofile.PostgreSQL:
		return "SELECT current_schema()", true
	case engineprofile.MySQL:
		return "SELECT DATABASE()", true
	case engineprofile.SQLite:
		return "SELECT name FROM pragma_database_list WHERE seq = 0", true
	default:
		return "", false
	}
}

// Default runs DefaultQuery against q and returns what the server answered. It returns the empty
// string, and no error, for an engine with no such statement and for a server that answers NULL:
// MySQL's DATABASE() is NULL on a connection that has selected no database, and PostgreSQL's
// current_schema() is NULL when search_path names no schema that exists.
//
// q is engineprofile.Queryer, which both *sql.DB and *sql.Tx implement, so a caller holding
// either can run this: internal/schemasource holds a *sql.DB and cli/rasqlmigrate's dump command
// holds the *sql.Tx it reads the rest of the dump through.
//
// `q` must not be nil.
func Default(ctx context.Context, q engineprofile.Queryer, engine engineprofile.EngineID) (string, error) {
	query, ok := DefaultQuery(engine)
	if !ok {
		return "", nil
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("db namespace: read default namespace: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var name sql.NullString
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("db namespace: read default namespace: %w", err)
		}
		return "", fmt.Errorf("db namespace: read default namespace: expected one row")
	}
	if err := rows.Scan(&name); err != nil {
		return "", fmt.Errorf("db namespace: read default namespace: %w", err)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("db namespace: read default namespace: %w", err)
	}
	return name.String, nil
}

// Unqualify clears Schema on every descriptor that names connected, and clears ReferencedSchema
// on every foreign key that names it, editing tables in place and returning it.
//
// A generated store renders the namespace it was read with into every statement built from the
// descriptor, so a table read out of the namespace the connection is already using would render
// as "main"."users" where the same read against PostgreSQL renders "users". The three engines
// report that namespace differently -- inspect records the SQLite database name and leaves Schema
// empty for a PostgreSQL or MySQL default enumeration -- so this is where both the generate path
// and the dump path settle on one answer for all of them: what the connection already resolves an
// unqualified name against is not written into the descriptor.
//
// A namespace the connection is not using survives. A SQLite database attached under another
// name, and a PostgreSQL schema outside search_path, both keep being named, because a statement
// built from that descriptor has to name them to reach the table.
//
// An empty namespace clears nothing, which is what a server that answered NULL leaves.
func Unqualify(tables []schema.TableDef, connected string) []schema.TableDef {
	if connected == "" {
		return tables
	}
	for i := range tables {
		if tables[i].Schema == connected {
			tables[i].Schema = ""
		}
		for j := range tables[i].ForeignKeys {
			if tables[i].ForeignKeys[j].ReferencedSchema == connected {
				tables[i].ForeignKeys[j].ReferencedSchema = ""
			}
		}
	}
	return tables
}
