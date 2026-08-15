package generate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/internal/genfile"
)

// writeGeneratedFile and removeGeneratedFile are the seam Plan.Commit writes
// and deletes through. They exist so a test can swap them for a recording
// stand-in and observe the order Commit calls them in, without handing a
// callback to any caller: both vars are unexported, so nothing outside this
// package can reach them. cli/rasqlgen/rasqlgen.go:27 uses the same pattern
// for openDatabase.
var (
	writeGeneratedFile  = genfile.Write
	removeGeneratedFile = os.Remove
)

// Plan is a rendered, uncommitted store package: every file's final bytes,
// every destination already resolved, and every leftover already
// identified.
//
// A Plan is a decision made at one instant. It reads the output directory
// once, when Store.Plan builds it. A file that appears in the directory
// afterwards is not reflected here; call Store.Plan again to see the
// directory as it is now.
//
// The zero Plan is not a plan: its Files and Orphans report empty, Commit
// refuses it naming Store.Plan, and nothing but Store.Plan builds one that
// reports or commits anything else.
type Plan struct {
	files   []File
	orphans []string
	// dir is the store's resolved output directory: the directory every
	// File.Path is a direct child of. It is empty only for the zero Plan,
	// which is what Commit checks to tell the two apart.
	dir string
	// prune is the Store's own Prune field, carried into the plan at
	// Store.Plan time so Commit's decision -- refuse the orphans or delete
	// them -- is made from the plan alone, the same instant everything
	// else about the plan was decided.
	prune bool
}

// File is one rendered output file. Source is the whole file, including the
// generated-code marker on its first line, already gofmt-formatted.
type File struct {
	// Path is the destination as planned, cleaned and absolute: the
	// store's resolved Dir joined with this file's own name. It is the
	// path a commit hands to the writer, which is not always the file the
	// bytes land in; see Resolved.
	Path string
	// Resolved is the file a commit would actually replace: Path with
	// every symbolic link along it followed, which is the destination
	// internal/genfile.Write writes through to. It equals Path for an
	// ordinary destination, and differs when Path itself, or a directory
	// holding it, is a symbolic link. Writing through such a link is
	// deliberate, so a Resolved outside the store's Dir is reported here
	// rather than refused. It is unique across a plan's files: Store.Plan
	// rejects two files that resolve to one destination.
	Resolved string
	// Source is the file's complete contents.
	Source []byte
}

// Files reports every file the plan writes, sorted by Path. The result and
// each File.Source are copies, so a caller cannot reach into the plan.
func (p Plan) Files() []File {
	files := make([]File, len(p.files))
	for i, f := range p.files {
		files[i] = File{Path: f.Path, Resolved: f.Resolved, Source: append([]byte(nil), f.Source...)}
	}
	return files
}

// Orphans reports every file already in the output directory that rasqlgen
// wrote and this plan does not write, sorted by path: the per-table file of
// a dropped table, the output of a query that was removed, or a file
// another tool wrote with rasqlgen's own marker.
func (p Plan) Orphans() []string {
	return append([]string(nil), p.orphans...)
}

// Commit writes every file in the plan and deletes every path Orphans
// reports, in four steps, so that a failure before the first write leaves
// the directory exactly as it was and every individual file is either its
// previous content or its new content, never a partial write.
//
//  1. Resolve and authorize everything; write nothing. The plan's output
//     directory is created when missing (os.MkdirAll, 0o700). Every planned
//     file's destination is re-resolved through genfile.ResolveDestination,
//     and every recorded orphan is re-read to confirm rasqlgen's marker
//     still stands on its first line -- both checked fresh here rather than
//     trusted from Plan time, since a Plan can be held and acted on later,
//     after the directory has changed underneath it. When Prune is false
//     and there is at least one orphan, Commit refuses here, naming every
//     one of them.
//  2. Write every per-table file and every query file, in path order.
//  3. Delete this run's leftovers: every path Orphans reported, in path
//     order, and only when Prune is set -- otherwise step 1 already
//     refused the run.
//  4. Write the aggregator last: schema_gen.go, then schema_gen_test.go.
//     The aggregator is the store's whole record of the schema -- every
//     descriptor literal and Tables() -- so until this step lands, the
//     record on disk is still the previous run's, complete and internally
//     consistent on its own.
//
// If the process dies partway through, every file on disk is either its
// previous content or its new content -- genfile.Write never truncates a
// destination in place, so nothing is ever a partial write -- but the
// package as a whole is not guaranteed to build in every window between
// steps 2 and 4. The per-table file for a table and the aggregator that
// declares the variable it returns name each other in both directions (see
// Store's own doc comment), so adding or removing a table needs both files
// to change together, and no ordering of separate file writes changes
// that: renaming two files as one operation is not something the
// filesystem offers. A run that adds a table can fail to build between
// steps 2 and 4 on an undefined per-table variable the old aggregator does
// not yet declare; a run that removes a table can fail to build the same
// way on a variable the old aggregator still declares after step 3 deleted
// the file that used it. This is the honest guarantee: every file is old
// or new, never partial, but the package in between is not promised to
// compile.
//
// Recovery is to rerun the generator: every input lives in the caller's
// own program, its migrations, or the database, none of which Commit
// touches. A store that crashed mid-write cannot supply its own input,
// because reading its Tables() means compiling it first; recover from the
// database, or with "git checkout -- <dir>", instead.
func (p Plan) Commit() error {
	if p.dir == "" {
		return errors.New("generate: zero Plan cannot be committed; only Store.Plan builds a Plan that Commit can act on")
	}

	// Step 1: resolve and authorize everything; write nothing.
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return fmt.Errorf("generate: create %s: %w", p.dir, err)
	}
	for _, f := range p.files {
		if _, err := genfile.ResolveDestination(f.Path); err != nil {
			return err
		}
	}
	for _, orphan := range p.orphans {
		marker, err := hasGenfileMarker(orphan)
		if err != nil {
			return fmt.Errorf("generate: check %s for rasqlgen's marker: %w", orphan, err)
		}
		if !marker {
			return fmt.Errorf("generate: refusing to delete %s: it no longer opens with rasqlgen's marker; something has changed it since Store.Plan ran", orphan)
		}
	}
	if !p.prune && len(p.orphans) > 0 {
		return fmt.Errorf("generate: %s holds %d file(s) rasqlgen wrote that this plan does not write, and Store.Prune is false: %s; set Prune to delete them, or remove them yourself", p.dir, len(p.orphans), strings.Join(p.orphans, ", "))
	}

	// The aggregator files are singled out here, once, so steps 2 and 4
	// below can write in the order Commit's own doc comment promises:
	// every per-table and query file first, the aggregator last. p.files
	// is already sorted by Path (Store.Plan sorts it), and that order is
	// preserved within each group below.
	var descriptor, descriptorTest *File
	rest := make([]File, 0, len(p.files))
	for i := range p.files {
		switch filepath.Base(p.files[i].Path) {
		case schemaDescriptorFilename:
			descriptor = &p.files[i]
		case schemaDescriptorTestFilename:
			descriptorTest = &p.files[i]
		default:
			rest = append(rest, p.files[i])
		}
	}
	if descriptor == nil || descriptorTest == nil {
		return fmt.Errorf("generate: internal error: plan for %s is missing its own %s or %s", p.dir, schemaDescriptorFilename, schemaDescriptorTestFilename)
	}

	// Step 2: write every per-table file and every query file.
	for _, f := range rest {
		if err := writeGeneratedFile(f.Path, f.Source); err != nil {
			return fmt.Errorf("generate: write %s: %w", f.Path, err)
		}
	}

	// Step 3: delete this run's leftovers, only when Prune is set --
	// otherwise step 1 already refused the run above.
	if p.prune {
		for _, orphan := range p.orphans {
			if err := removeGeneratedFile(orphan); err != nil {
				return fmt.Errorf("generate: delete %s: %w", orphan, err)
			}
		}
	}

	// Step 4: write the aggregator last.
	if err := writeGeneratedFile(descriptor.Path, descriptor.Source); err != nil {
		return fmt.Errorf("generate: write %s: %w", descriptor.Path, err)
	}
	if err := writeGeneratedFile(descriptorTest.Path, descriptorTest.Source); err != nil {
		return fmt.Errorf("generate: write %s: %w", descriptorTest.Path, err)
	}
	return nil
}

// findOrphans reports every leftover file in dir: a regular file directly in
// dir, whose name ends in _gen.go or _gen_test.go, whose first line is
// exactly genfile.Marker, and whose path is not one of planned's own paths.
// A missing dir reports no orphans, matching Store.Plan's promise not to
// create anything.
func findOrphans(dir string, planned []File) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	own := make(map[string]struct{}, len(planned))
	for _, f := range planned {
		own[f.Path] = struct{}{}
	}

	orphans := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		if _, isOwn := own[path]; isOwn {
			continue
		}
		marker, err := hasGenfileMarker(path)
		if err != nil {
			return nil, err
		}
		if !marker {
			continue
		}
		orphans = append(orphans, path)
	}
	sort.Strings(orphans)
	return orphans, nil
}

// hasGenfileMarker reports whether path opens with genfile.Marker standing
// alone on its first line, the same test internal/genfile applies before it
// will ever overwrite an existing destination. It is duplicated here, rather
// than exported from internal/genfile, because internal/genfile's own check
// is a side effect of resolving a destination to write, and this call must
// never write anything.
func hasGenfileMarker(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = file.Close() }()

	head := make([]byte, len(genfile.Marker)+1)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	head = head[:n]
	if !bytes.HasPrefix(head, []byte(genfile.Marker)) {
		return false, nil
	}
	rest := head[len(genfile.Marker):]
	return len(rest) == 0 || rest[0] == '\n' || rest[0] == '\r', nil
}
