package generate

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/internal/genfile"
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
// The zero Plan is not a plan: its Files and Orphans report empty, and
// nothing but Store.Plan builds one that reports anything else.
type Plan struct {
	files   []File
	orphans []string
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
	// rather than refused.
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
