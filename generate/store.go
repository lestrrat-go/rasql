package generate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/lestrrat-go/rasql/internal/querygen"
)

const maxQueryInputBytes = 64 << 20

type queryInputSnapshot struct {
	path   string
	digest [sha256.Size]byte
}

// Store describes one generated store package: which tables it is
// generated from, where it goes, and what else belongs in the same
// directory.
//
// A Store is a value and holds no state of its own. It touches neither the
// filesystem nor a database until one of its methods runs, and none of its
// methods mutates it, so two field-wise equal Stores plan byte-identical
// output.
type Store struct {
	compact *compactStoreInput
	// Package is the generated package name. Required, and must be a Go
	// identifier that is not the blank identifier: "package _" is not a
	// package clause the compiler accepts.
	Package string

	// Dir is the directory the package is written into, resolved against
	// Root when relative. Required. A later Write creates it, and every
	// parent, when it does not exist.
	Dir string

	// Root is the directory relative paths in Dir and in each Query
	// resolve against. Empty means the module root: the directory
	// holding the nearest go.mod at or above the process working
	// directory. That default is what makes Dir mean the same thing
	// wherever the //go:generate line that runs this program happens to
	// live. When Root is empty and no go.mod is found, a relative path
	// is an error rather than a guess.
	Root string

	// The legacy fields remain private only for the pinned historical
	// generator comparison compiled by this package's tests.

	TypedQueries []TypedQuery

	// Prune allows a run to delete a file in Dir that rasqlgen wrote and
	// this plan does not write -- the per-table file of a table that was
	// dropped, say. False refuses the run instead, naming every such
	// file. See Plan.Orphans.
	Prune bool
}

// TypedQuery describes lock-backed native SQL for offline generation.
type TypedQuery struct {
	Function      string
	Output        string
	Engine        string
	SQL           string
	Operation     string
	Cardinality   string
	Result        string
	Projection    string
	Decoder       string
	Parameters    []querygen.TypedValue
	Results       []querygen.TypedValue
	Imports       []compilerir.GoImport
	ArgumentNames []string
}

func (s Store) Plan() (Plan, error) { return s.PlanContext(context.Background()) }

// PlanContext plans the store and runs configured result describers.
func (s Store) PlanContext(ctx context.Context) (Plan, error) {
	// The blank identifier is checked separately because
	// token.IsIdentifier accepts it: it is an identifier everywhere else
	// in Go, but "package _" is not a package clause the compiler
	// accepts, so a plan built from it renders files that cannot build.
	// Nothing narrower is refused here. A keyword such as "func" is
	// already refused by token.IsIdentifier, and "package init" is a
	// legal package clause, so it stays accepted.
	if s.Package == "_" {
		return Plan{}, errors.New("generate: store package cannot be the blank identifier")
	}
	if !token.IsIdentifier(s.Package) {
		return Plan{}, fmt.Errorf("generate: store package %q must be a Go identifier", s.Package)
	}
	if s.Dir == "" {
		return Plan{}, errors.New("generate: store requires Dir")
	}
	if s.compact != nil {
		return s.planCompactContext(ctx)
	}
	return Plan{}, errors.New("generate: store was not produced by an emitter")
}

// Write plans the store and commits the plan: it is Plan followed by
// Plan.Commit, and creates Dir, with any missing parent, when it does not
// exist. See Plan.Commit for the write order, what is true on disk after
// each step, and the honest limit of what a partial failure leaves behind.
func (s Store) Write() error {
	plan, err := s.Plan()
	if err != nil {
		return err
	}
	return plan.Commit()
}

// Check plans the store and compares the plan with what is on disk, without
// writing anything. It returns nil when a Write would change nothing at
// all, an error wrapping ErrStale when the generated package differs from
// what the current inputs produce, and the error Commit itself would return
// when Commit would refuse the run instead of writing anything. A held Plan
// guards file-backed query inputs by their captured bytes; a fresh Store.Check
// evaluates the current inputs. See Plan.Check.
func (s Store) Check() error {
	plan, err := s.Plan()
	if err != nil {
		return err
	}
	return plan.Check()
}

func (s Store) planTypedQuery(dir string, q TypedQuery, filenames, identifiers map[string]string) (File, error) {
	if !isExportedGoIdentifier(q.Function) {
		return File{}, fmt.Errorf("function %q must be exported", q.Function)
	}
	if q.Output == "" || !strings.HasSuffix(q.Output, "_gen.go") {
		return File{}, fmt.Errorf("query %q output %q must end in _gen.go", q.Function, q.Output)
	}
	if err := validateQueryOutputName(q.Output); err != nil {
		return File{}, err
	}
	if owner, exists := identifiers[q.Function]; exists {
		return File{}, fmt.Errorf("function %q collides with %s", q.Function, owner)
	}
	if owner, exists := filenames[filenameKey(q.Output)]; exists {
		return File{}, fmt.Errorf("query %q output %q collides with %s", q.Function, q.Output, owner)
	}
	result := q.Result
	if result == "" && q.Operation != "exec" {
		result = q.Function + "Result"
	}
	queryNames := map[string]string{q.Function: "function"}
	for label, name := range map[string]string{"result": result, "projection": q.Projection, "decoder": q.Decoder} {
		if name == "" {
			continue
		}
		if previous, exists := queryNames[name]; exists {
			return File{}, fmt.Errorf("query %q %s %q collides with its %s declaration", q.Function, label, name, previous)
		}
		queryNames[name] = label
	}
	for _, name := range []string{result, q.Projection, q.Decoder} {
		if name == "" {
			continue
		}
		if owner, exists := identifiers[name]; exists {
			return File{}, fmt.Errorf("query %q generated identifier %q collides with %s", q.Function, name, owner)
		}
	}
	source, err := querygen.TypedGoSource(querygen.TypedInput{
		Package: s.Package, Function: q.Function, Engine: q.Engine, SQL: q.SQL,
		Operation: q.Operation, Cardinality: q.Cardinality, Result: result,
		Projection: q.Projection, Decoder: q.Decoder, Parameters: q.Parameters, Results: q.Results, Imports: q.Imports,
		ArgumentNames: q.ArgumentNames,
	})
	if err != nil {
		return File{}, err
	}
	filenames[filenameKey(q.Output)] = fmt.Sprintf("query %q, which generates %s", q.Function, q.Output)
	identifiers[q.Function] = fmt.Sprintf("query %q", q.Function)
	for _, name := range []string{result, q.Projection, q.Decoder} {
		if name != "" {
			identifiers[name] = fmt.Sprintf("query %q declaration", q.Function)
		}
	}
	return File{Path: filepath.Join(dir, q.Output), Source: source}, nil
}

// readQueryInput reads a query through a bounded reader. A size check before
// reading is not enough because a path can name a fifo or another stream that
// reports no useful size. Reading one byte beyond the limit catches both
// regular files and streaming inputs without allocating unbounded memory.
func readQueryInput(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, int64(maxQueryInputBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxQueryInputBytes {
		return nil, fmt.Errorf("input file %s exceeds maximum size of %d bytes", path, maxQueryInputBytes)
	}
	return data, nil
}

// validateQueryOutputName checks that output is the plain file name
// Query.Output documents itself to be, rather than any kind of path.
//
// The check is load-bearing twice over. The collision check above is keyed on
// the name while the destination is built with filepath.Join, which cleans
// it, so an unvalidated "nested/../users_gen.go" matches no key the users
// table registered yet lands on that table's own path: two planned files, one
// destination. And filepath.Join keeps whatever a cleaned "../" or an
// absolute path resolves to, so the same gap lets a planned File.Path leave
// Dir entirely. Requiring the name to be its own filepath.Base closes both,
// and rejects "sub/x_gen.go" with them, since Plan creates no directory and a
// later run writing into one would have to.
func validateQueryOutputName(output string) error {
	if output != filepath.Base(output) {
		return fmt.Errorf("output %q must be a file name directly inside the store's Dir, not a path", output)
	}
	return nil
}

// filenameKey reports the key two generated file names are one claim under.
//
// Case is folded away, on every platform. A filesystem that ignores case in
// file names -- macOS's default APFS, and NTFS -- holds a single entry for
// a_gen.go and A_gen.go, so a store planning both writes one file there and
// loses the other, and Plan cannot ask the filesystem which it is dealing
// with: neither name exists yet when this check runs. Folding everywhere
// rather than only where it matters also keeps one Store planning the same
// package wherever it runs, which is the point of a generated package that
// is checked in and built on whatever machine checks it out.
//
// Only Query.Output can reach here spelled in mixed case. A table's file
// name comes from schemaOutputFilename, which lowercases the table name, and
// the descriptor file names are lowercase constants.
func filenameKey(name string) string {
	return strings.ToLower(name)
}

// isExportedGoIdentifier reports whether name is a valid, exported Go
// identifier: an ordinary Go identifier whose first rune is uppercase.
func isExportedGoIdentifier(name string) bool {
	if !token.IsIdentifier(name) {
		return false
	}
	first, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(first)
}

func resolveModuleRoot(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	root, err := modroot.FromWorkingDirectory()
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}
	return root, nil
}

// resolveStorePath resolves path against root when path is relative. An
// absolute path is returned unchanged. A relative path with an empty root
// -- Root left empty with no go.mod found above the working directory -- is
// an error naming Store.Root, since there is nothing to resolve it against.
func resolveStorePath(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if root == "" {
		return "", fmt.Errorf("relative path %q cannot be resolved: Store.Root is empty and no go.mod was found above the working directory", path)
	}
	return filepath.Join(root, path), nil
}
