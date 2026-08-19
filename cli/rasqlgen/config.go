package rasqlgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/lestrrat-go/rasql/schema"
)

// defaultConfigName is the file a run reads when -config names none. It sits
// at the module root, which is also what a relative Output and a relative
// query Input resolve against, so every path in the file reads from the
// place the file itself lives.
const defaultConfigName = "rasql.json"

// maxConfigBytes bounds the configuration file a run will read. A
// configuration file is a hand-written page of settings; anything past this
// is a wrong path pointed at a data file, and reading it whole first would
// be the only way to find that out.
const maxConfigBytes = 1 << 20

// config is the project's generation settings, read from JSON.
//
// It holds what stays the same from run to run. What changes per run stays
// on the command line: -dsn, because it carries a credential and this file
// is checked in, and -check, because it selects what one run does rather
// than what the project is.
//
// Every field is optional here and required later, so a file may state only
// what it overrides and a flag may supply the rest. runGenerate reports what
// is still missing once the two are merged.
type config struct {
	// Package is the generated package name.
	Package string `json:"package"`

	// Output is the generated package's directory, resolved against Root.
	Output string `json:"output"`

	// Root is the directory a relative Output and a relative query Input
	// resolve against. Empty means the module root above the working
	// directory, which is where this file normally sits.
	Root string `json:"root"`

	// Dialect is the SQL dialect: postgresql (or postgres), mysql, or
	// sqlite.
	Dialect string `json:"dialect"`

	// Prune allows a run to delete a generated file it no longer writes.
	// It is a pointer so that a file stating false is distinguishable from
	// a file stating nothing, since the default is true.
	Prune *bool `json:"prune"`

	// Tables selects and names the tables the store is generated from.
	Tables configTables `json:"tables"`

	// Queries are static SQL templates compiled into the generated package.
	Queries []configQuery `json:"queries"`
}

// configTables is the table selection and the Go-side names no database can
// state.
type configTables struct {
	// Include names the only tables to generate. Empty sweeps every base
	// table. It is not accepted together with Exclude.
	Include []string `json:"include"`

	// Exclude names tables to skip.
	Exclude []string `json:"exclude"`

	// HistoryTable names the migration history table to skip when it is not
	// the default rasql_schema_migrations.
	HistoryTable string `json:"history_table"`

	// RowNames overrides the generated row type of a table, keyed by table
	// name. The generator derives <Table>Row on its own; state a name here
	// to read better, or to break a collision between one table's derived
	// row name and another table's generated names, which refuses the run.
	RowNames map[string]string `json:"row_names"`
}

// configQuery is one static SQL template compiled into a generated function.
//
// The template lives either in its own file, named by Input, or in this file,
// written into SQL. A file keeps SQL in a file an editor, a formatter and a
// query runner all recognize as SQL, which a JSON string is not, and it holds
// a multi-line statement as the lines it was written as. Writing the template
// here keeps a one-line query in one place, at the cost of escaping every
// quote the {{bind "name"}} action needs.
type configQuery struct {
	// Input is the template file, resolved against Root when relative.
	// State exactly one of Input and SQL.
	Input string `json:"input"`

	// SQL is the template itself. State exactly one of Input and SQL.
	SQL string `json:"sql"`

	// Function is the generated function name, which must be exported.
	Function string `json:"function"`

	// Output is the file the function is generated into, a file name
	// directly inside the generated package's directory. Empty derives it
	// from Input's base name, so queries/user_by_email.sql becomes
	// user_by_email_gen.go, and from Function for a query stating SQL, so
	// UserByEmail becomes user_by_email_gen.go as well.
	Output string `json:"output"`
}

// loadConfig reads the configuration for one run. path is -config as given,
// which may be empty.
//
// An explicit -config that names nothing is an error, because a user who
// typed a path meant that file. The default file is optional instead: a
// project whose flags say everything needs no file at all, and a missing
// rasql.json is that project rather than a mistake.
func loadConfig(path string) (config, error) {
	explicit := path != ""
	if !explicit {
		root, err := modroot.FromWorkingDirectory()
		if err != nil {
			return config{}, fmt.Errorf("generate: %w", err)
		}
		if root == "" {
			return config{}, nil
		}
		path = filepath.Join(root, defaultConfigName)
	}

	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		return config{}, nil
	case err != nil:
		return config{}, fmt.Errorf("generate: read config %s: %w", path, err)
	case info.IsDir():
		return config{}, fmt.Errorf("generate: config %s is a directory", path)
	case info.Size() > maxConfigBytes:
		return config{}, fmt.Errorf("generate: config %s is %d bytes, past the %d-byte limit; -config expects a settings file", path, info.Size(), maxConfigBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("generate: read config %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	// A misspelled key is a setting that silently does nothing, which is
	// the failure a configuration file is worst at showing. Rejecting the
	// key turns it into a message naming it.
	decoder.DisallowUnknownFields()
	var loaded config
	if err := decoder.Decode(&loaded); err != nil {
		return config{}, fmt.Errorf("generate: parse config %s: %w", path, err)
	}
	if decoder.More() {
		return config{}, fmt.Errorf("generate: parse config %s: unexpected value after the settings object", path)
	}
	return loaded, nil
}

// hints turns the row-name overrides into the map generate.Store takes. A
// blank override is refused rather than ignored, since a key written with no
// value is a half-finished edit rather than a request for the default.
func (c config) hints() (map[string]schema.TableHint, error) {
	if len(c.Tables.RowNames) == 0 {
		return nil, nil
	}
	hints := make(map[string]schema.TableHint, len(c.Tables.RowNames))
	for table, rowName := range c.Tables.RowNames {
		if table == "" {
			return nil, errors.New("generate: config states a row name for an empty table name")
		}
		if rowName == "" {
			return nil, fmt.Errorf("generate: config states an empty row name for table %q", table)
		}
		hints[table] = schema.TableHint{RowName: rowName}
	}
	return hints, nil
}

// queries turns the configured templates into generate.Query values,
// deriving each output file name from its input when the file states none.
func (c config) queries() ([]generate.Query, error) {
	if len(c.Queries) == 0 {
		return nil, nil
	}
	queries := make([]generate.Query, len(c.Queries))
	for index, query := range c.Queries {
		if query.Input == "" && query.SQL == "" {
			return nil, fmt.Errorf("generate: config query %d states neither input nor sql", index+1)
		}
		if query.Input != "" && query.SQL != "" {
			return nil, fmt.Errorf("generate: config query %d states both input and sql; a query's template lives in one of them", index+1)
		}
		if query.Function == "" {
			return nil, fmt.Errorf("generate: config query %d states no function", index+1)
		}
		output := query.Output
		switch {
		case output != "":
		case query.Input != "":
			output = derivedQueryOutput(query.Input)
		default:
			output = snakeCase(query.Function) + "_gen.go"
		}
		queries[index] = generate.Query{Input: query.Input, SQL: query.SQL, Function: query.Function, Output: output}
	}
	return queries, nil
}

// derivedQueryOutput names the generated file for a query that states none:
// the input's base name with its extension replaced by _gen.go, so
// queries/user_by_email.sql becomes user_by_email_gen.go beside the rest of
// the generated package.
func derivedQueryOutput(input string) string {
	base := filepath.Base(filepath.FromSlash(input))
	return base[:len(base)-len(filepath.Ext(base))] + "_gen.go"
}

// snakeCase names the generated file for a query that states its template
// inline and names no output: the function name lowered, with an underscore
// before each word after the first, so UserByEmail becomes user_by_email and
// UserByID becomes user_by_id.
func snakeCase(name string) string {
	runes := []rune(name)
	var result strings.Builder
	result.Grow(len(name) + 4)
	for index, current := range runes {
		if index > 0 && unicode.IsUpper(current) &&
			(!unicode.IsUpper(runes[index-1]) ||
				(index+1 < len(runes) && unicode.IsLower(runes[index+1]))) {
			result.WriteByte('_')
		}
		result.WriteRune(unicode.ToLower(current))
	}
	return result.String()
}
