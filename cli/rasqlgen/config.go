package rasqlgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/internal/compilerconfig"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
)

// defaultConfigName is the file a run reads when -config names none. It sits
// at the module root, which is also what a relative Output and a relative
// query Input resolve against, so every path in the file reads from the
// place the file itself lives.
const defaultConfigName = "rasql.json"

// maxConfigBytes bounds the configuration file a run will read. A
// configuration file is a hand-written page of settings; anything past this
// is a wrong path pointed at a data file, and the actual read is bounded so it
// cannot consume the whole file before finding that out.
const maxConfigBytes = 1 << 20

// config is the project's generation settings, read from JSON.
//
// It holds what stays the same from run to run. Credentials remain on the
// command line, and -check selects what one run does.
type config struct {
	// Engine and Schema select the lock-backed schema workflow.
	Engine *schemaEngineConfig `json:"engine"`
	Schema *schemaSourceConfig `json:"schema"`
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
	Emitter string `json:"emitter"`

	// Prune allows a run to delete a generated file it no longer writes.
	// It is a pointer so that a file stating false is distinguishable from
	// a file stating nothing, since the default is true.
	Prune *bool `json:"prune"`

	// Tables selects and names the tables the store is generated from.
	Tables configTables `json:"tables"`

	// Queries are static SQL templates compiled into the generated package.
	Queries []configQuery `json:"queries"`

	// Mappings names explicit semantic, Go, codec, and NULL mappings.
	// It remains raw until package validation has supplied the generated package name.
	Mappings json.RawMessage `json:"mappings"`
}

type schemaEngineConfig struct {
	Dialect string `json:"dialect"`
	Profile string `json:"profile"`
}

type schemaSourceConfig struct {
	Kind        string            `json:"kind"`
	Identity    string            `json:"identity"`
	Paths       []string          `json:"paths"`
	Inputs      []string          `json:"inputs"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
}

func (c config) mappings() (compilerir.MappingConfig, error) {
	if len(c.Mappings) == 0 {
		return compilerir.MappingConfig{}, nil
	}
	return compilerconfig.DecodeMappings(c.Mappings, c.Package)
}

// configTables is the table selection and the Go-side names no database can
// state.
type configTables struct {
	Namespaces []string `json:"namespaces"`

	IncludeObjects []schema.ObjectName `json:"include_objects"`

	ExcludeObjects []schema.ObjectName `json:"exclude_objects"`

	IncludeViews bool `json:"include_views"`
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

	Names map[string]configObjectNames `json:"names"`
}

type configObjectNames struct {
	Accessor  string                       `json:"accessor,omitempty"`
	TableType string                       `json:"table_type,omitempty"`
	RowType   string                       `json:"row_type,omitempty"`
	FileBase  string                       `json:"file_base,omitempty"`
	Columns   map[string]configColumnNames `json:"columns,omitempty"`
}

type configColumnNames struct {
	Field    string `json:"field,omitempty"`
	Accessor string `json:"accessor,omitempty"`
}

func (c config) names() (map[schema.ObjectName]configObjectNames, error) {
	if len(c.Tables.Names) == 0 {
		return nil, nil
	}
	result := make(map[schema.ObjectName]configObjectNames, len(c.Tables.Names))
	for identity, names := range c.Tables.Names {
		parts := strings.Split(identity, ".")
		switch {
		case len(parts) == 1 && parts[0] != "":
			result[schema.ObjectName{Name: parts[0]}] = names
		case len(parts) == 2 && parts[0] != "" && parts[1] != "":
			result[schema.ObjectName{Schema: parts[0], Name: parts[1]}] = names
		default:
			return nil, fmt.Errorf("generate: config names key %q must be table or namespace.table", identity)
		}
	}
	return result, nil
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
	ID          compilerir.QueryID               `json:"id"`
	Engine      string                           `json:"engine"`
	Operation   string                           `json:"operation"`
	Cardinality string                           `json:"cardinality"`
	Parameters  []compilerquery.ValueDeclaration `json:"parameters"`
	Results     []compilerquery.ValueDeclaration `json:"results"`
	// Bindings configures explicit Go types for static-query parameters.
	Bindings map[string]namedsql.ParameterBinding `json:"bindings"`

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

	file, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("generate: read config %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxConfigBytes)+1))
	if err != nil {
		return config{}, fmt.Errorf("generate: read config %s: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return config{}, fmt.Errorf(
			"generate: config %s exceeds the %d-byte limit; -config expects a settings file",
			path, maxConfigBytes,
		)
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
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return config{}, fmt.Errorf("generate: parse config %s: unexpected value after the settings object", path)
		}
		return config{}, fmt.Errorf("generate: parse config %s: unexpected value after the settings object", path)
	}
	if _, err := loaded.mappings(); err != nil {
		return config{}, fmt.Errorf("generate: parse config %s mappings: %w", path, err)
	}
	if loaded.Emitter != "" && loaded.Emitter != "compact" {
		return config{}, fmt.Errorf("generate: config emitter %q must be compact", loaded.Emitter)
	}
	return loaded, nil
}

func (c config) compilerQueries(root string) compilerquery.Config {
	queries := make([]compilerquery.QueryConfig, len(c.Queries))
	for i, query := range c.Queries {
		queries[i] = compilerquery.QueryConfig{ID: query.ID, Input: query.Input, Engine: query.Engine, Function: query.Function, Output: query.Output, Operation: query.Operation, Cardinality: query.Cardinality, Parameters: query.Parameters, Results: query.Results}
	}
	mappings, _ := c.mappings()
	return compilerquery.Config{ModuleRoot: root, Mappings: mappings, Queries: queries}
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
