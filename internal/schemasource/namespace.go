package schemasource

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
)

// defaultNamespaceQuery reports the statement that asks a server which namespace it is
// connected to, and false for an engine this package has no such statement for.
//
// PostgreSQL answers with current_schema(), MySQL with DATABASE(), and SQLite with the name
// at sequence 0 of PRAGMA database_list, which is the database a CREATE TABLE carrying no
// qualifier writes into. Each statement returns exactly one row of one column.
func defaultNamespaceQuery(p engineprofile.Profile) (string, bool) {
	switch p.Engine {
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

// defaultNamespace runs defaultNamespaceQuery against db and returns what the server
// answered. It returns the empty string, and no error, for an engine with no such statement
// and for a server that answers NULL: MySQL's DATABASE() is NULL on a connection that has
// selected no database, and PostgreSQL's current_schema() is NULL when search_path names no
// schema that exists.
//
// `db` must not be nil.
func defaultNamespace(ctx context.Context, db *sql.DB, p engineprofile.Profile) (string, error) {
	query, ok := defaultNamespaceQuery(p)
	if !ok {
		return "", nil
	}
	var name sql.NullString
	if err := db.QueryRowContext(ctx, query).Scan(&name); err != nil {
		return "", fmt.Errorf("schema source: read default namespace: %w", err)
	}
	return name.String, nil
}

// unqualifyDefaultNamespace clears Schema on every descriptor that names namespace, and
// clears ReferencedSchema on every foreign key that names it, editing tables in place and
// returning it.
//
// A generated store renders the namespace it was read with into every statement built from
// the descriptor, so a table read out of the namespace the connection is already using would
// render as "main"."users" where the same read against PostgreSQL renders "users". The three
// engines report that namespace differently -- inspect records the SQLite database name and
// leaves Schema empty for a PostgreSQL or MySQL default enumeration -- so this is where the
// generate path settles on one answer for all of them: what the connection already resolves
// an unqualified name against is not written into the descriptor.
//
// A namespace the connection is not using survives. A SQLite database attached under another
// name, and a PostgreSQL schema outside search_path, both keep being named, because a
// statement built from that descriptor has to name them to reach the table.
//
// An empty namespace clears nothing, which is what a server that answered NULL leaves.
func unqualifyDefaultNamespace(tables []schema.TableDef, namespace string) []schema.TableDef {
	if namespace == "" {
		return tables
	}
	for i := range tables {
		if tables[i].Schema == namespace {
			tables[i].Schema = ""
		}
		for j := range tables[i].ForeignKeys {
			if tables[i].ForeignKeys[j].ReferencedSchema == namespace {
				tables[i].ForeignKeys[j].ReferencedSchema = ""
			}
		}
	}
	return tables
}
