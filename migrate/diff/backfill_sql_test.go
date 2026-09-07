package diff

import (
	"errors"
	"testing"

	mysqlquery "github.com/lestrrat-go/rasql-mysql/query"
	pgquery "github.com/lestrrat-go/rasql-pg/query"
	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/stretchr/testify/require"
)

func TestValidateNativeSQLUsesDialectParsers(t *testing.T) {
	for _, test := range []struct {
		dialect string
		source  string
	}{
		{"postgresql", "/* note; */ SELECT 'value;';"},
		{"mysql", "-- note\nSELECT 'value;';"},
		{"sqlite", "SELECT 'value;'; -- note"},
		{"sqlite", "UPDATE tasks SET owner_label = 'owner-' || owner_id;"},
		{"sqlite", "-- leading note\nUPDATE \"tasks\" SET \"owner_label\" = 'semi;colon';"},
		{"sqlite", "UPDATE [tasks] SET [owner_label] = ('x' || owner_id) /* trailing */;"},
		{"postgresql", `UPDATE "tasks" SET "owner_label" = 'owner-' || "owner_id";`},
		{"postgresql", `UPDATE "tasks" SET "owner_label" = $value$semi;--not comment$value$;`},
		{"mysql", "UPDATE `tasks` SET `owner_label` = concat('owner;', `owner_id`); # trailing"},
		{"mysql", "UPDATE tasks SET owner_label = 'owner-' /* ; */;"},
		{"mysql", `UPDATE tasks SET owner_label = 'owner\';tail';`},
		{"sqlite", "UPDATE [tasks] SET [owner_label] = 'owner-' || [owner_id];"},
	} {
		if err := validateNativeSQL(test.dialect, test.source); err != nil {
			t.Errorf("%s: valid source rejected: %v", test.dialect, err)
		}
	}
	for _, test := range []struct {
		dialect string
		source  string
	}{
		{"postgresql", "SELECT 1; SELECT 2;"},
		{"mysql", "SELECT 1; SELECT 2;"},
		{"sqlite", "SELECT 1; SELECT 2;"},
		{"postgresql", "/* comment only */"},
		{"mysql", "-- comment only\n"},
		{"sqlite", "/* comment only */"},
		{"postgresql", "this is not SQL"},
		{"mysql", "this is not SQL"},
		{"sqlite", "this is not SQL"},
		{"sqlite", "UPDATE tasks SET owner_label = 'x'; DELETE FROM tasks;"},
		{"sqlite", "UPDATE tasks SET owner_label = 'unterminated;"},
		{"sqlite", "UPDATE tasks SET owner_label = (owner_id;"},
		{"sqlite", "UPDATE tasks owner_label = 'x';"},
		{"sqlite", "UPDATE;"},
		{"postgresql", "UPDATE tasks SET owner_label = 'x'; DELETE FROM tasks;"},
		{"mysql", "UPDATE tasks SET owner_label = 'x', other = 1;"},
		{"postgresql", "UPDATE tasks SET owner_label = 'x' FROM source;"},
		{"mysql", "UPDATE tasks SET owner_label = `unterminated;"},
		{"postgresql", "UPDATE tasks SET owner_label = $value$unterminated;"},
		{"postgresql", "UPDATE tasks SET owner_label = (owner_id;"},
		{"mysql", "UPDATE tasks owner_label = 'x';"},
		{"mysql", "UPDATE tasks SET;"},
		{"postgresql", "UPDATE SET owner_label = 'x';"},
		{"mysql", "UPDATE SET owner_label = 'x';"},
		{"sqlite", "UPDATE SET owner_label = 'x';"},
		{"postgresql", "UPDATE tasks owner_label = 'x';"},
		{"postgresql", "UPDATE tasks SET;"},
		{"sqlite", "UPDATE tasks SET owner_label = 'x', other = 1;"},
		{"sqlite", "UPDATE tasks SET owner_label = 'x' FROM source;"},
		{"mysql", "UPDATE tasks SET owner_label = 'x' RETURNING id;"},
		{"postgresql", "UPDATE tasks SET owner_label = 'x' ORDER BY id;"},
		{"sqlite", "UPDATE tasks SET owner_label = 'x' LIMIT 1;"},
		{"postgresql", "UPDATE tasks SET owner_label = (owner_id;"},
		{"mysql", "UPDATE `tasks SET owner_label = 'x';"},
		{"postgresql", "UPDATE tasks SET owner_label = $tag$unterminated;"},
		{"postgresql", "UPDATE tasks SET owner_label;"},
		{"mysql", "UPDATE tasks SET owner_label;"},
		{"sqlite", "UPDATE tasks SET owner_label;"},
	} {
		if err := validateNativeSQL(test.dialect, test.source); err == nil {
			t.Errorf("%s: invalid source accepted: %q", test.dialect, test.source)
		}
	}
}

func TestUnsupportedNativeUpdateGateRequiresExactParserError(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		err     error
	}{
		{"postgres wrong type", "postgresql", errors.New("unsupported statement")},
		{"postgres nonzero offset", "postgresql", &pgquery.ParseError{Offset: 1, Message: "unsupported statement"}},
		{"postgres different message", "postgresql", &pgquery.ParseError{Offset: 0, Message: "expected expression"}},
		{"mysql wrong type", "mysql", errors.New("unsupported statement")},
		{"mysql nonzero offset", "mysql", &mysqlquery.ParseError{Offset: 1, Message: "unsupported statement"}},
		{"mysql different message", "mysql", &mysqlquery.ParseError{Offset: 0, Message: "expected expression"}},
		{"sqlite wrong type", "sqlite", errors.New("unsupported statement")},
		{"sqlite nonzero offset", "sqlite", &sqlitequery.ParseError{Offset: 1, Message: "unsupported statement"}},
		{"sqlite different message", "sqlite", &sqlitequery.ParseError{Offset: 0, Message: "expected expression"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if isUnsupportedNativeUpdate(test.dialect, test.err, "UPDATE tasks SET owner_label = 'x'") {
				t.Fatal("gate accepted a non-exact parser error")
			}
		})
	}
}

func TestValidateNativeSQLRejectsCommonUpdateShapesPerDialect(t *testing.T) {
	cases := map[string]string{
		"second statement":       "UPDATE tasks SET owner_label = 'x'; DELETE FROM tasks;",
		"missing target":         "UPDATE SET owner_label = 'x';",
		"missing SET":            "UPDATE tasks owner_label = 'x';",
		"missing equals":         "UPDATE tasks SET owner_label;",
		"empty right side":       "UPDATE tasks SET owner_label = ;",
		"top-level comma":        "UPDATE tasks SET owner_label = 'x', other = 1;",
		"top-level FROM":         "UPDATE tasks SET owner_label = 'x' FROM source;",
		"top-level RETURNING":    "UPDATE tasks SET owner_label = 'x' RETURNING id;",
		"top-level ORDER":        "UPDATE tasks SET owner_label = 'x' ORDER BY id;",
		"top-level LIMIT":        "UPDATE tasks SET owner_label = 'x' LIMIT 1;",
		"unterminated quote":     "UPDATE tasks SET owner_label = 'x;",
		"unterminated comment":   "UPDATE tasks SET owner_label = 'x' /* comment;",
		"unbalanced parentheses": "UPDATE tasks SET owner_label = (owner_id;",
	}
	for _, dialect := range []string{"postgresql", "mysql", "sqlite"} {
		for name, source := range cases {
			t.Run(dialect+"/"+name, func(t *testing.T) {
				err := validateNativeSQL(dialect, source)
				require.Error(t, err)
				require.Contains(t, err.Error(), "invalid "+map[string]string{"postgresql": "PostgreSQL", "mysql": "MySQL", "sqlite": "SQLite"}[dialect]+" SQL:")
			})
		}
	}
}
