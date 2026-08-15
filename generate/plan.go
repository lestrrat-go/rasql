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
//
// removeGeneratedFile deletes through an open directory handle and a bare
// file name rather than a path, so the deletion lands in the directory
// Commit already authorized. A path would be resolved again by the kernel,
// component by component, and a directory swapped for a symbolic link
// between the check and the delete would send that delete somewhere else.
var (
	writeGeneratedFile  = genfile.Write
	removeGeneratedFile = func(dir *os.Root, name string) error { return dir.Remove(name) }
)

// Plan is a rendered, uncommitted store package: every file's final bytes,
// every destination already resolved, and every leftover already
// identified.
//
// A Plan is a decision made at one instant. It reads the output directory
// once, when Store.Plan builds it. A file that appears in the directory
// afterwards is not reflected here; call Store.Plan again to see the
// directory as it is now. The directory itself is the one exception: a plan
// records which directory it read, and Commit refuses to act when the path
// no longer names that same directory, since every orphan it would delete
// was recorded relative to it.
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
	// dirInfo is what the filesystem reported for dir at the instant
	// Store.Plan listed it, or nil when dir did not exist yet and the plan
	// therefore recorded no orphans at all. Commit compares it, with
	// os.SameFile, against the directory the same path reaches at commit
	// time, so a dir that has since been replaced by a symbolic link to
	// somewhere else is refused instead of having this plan's deletions
	// applied to whatever the link points at. A path string alone cannot
	// carry that: every component of it is resolved afresh by the kernel
	// on each call, so the same string is a different directory once
	// something along it changes.
	dirInfo fs.FileInfo
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
	// rejects two files that resolve to one destination, and asks the
	// filesystem whether two spellings are one file rather than comparing
	// the two strings.
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
//
// A file the plan does write is never one of these, whatever it is spelled
// as on disk. On a filesystem that ignores case in file names, a directory
// holding Q_gen.go and a plan writing q_gen.go name one file, and it is the
// planned file rather than a leftover.
func (p Plan) Orphans() []string {
	return append([]string(nil), p.orphans...)
}

// Commit writes every file in the plan and deletes every path Orphans
// reports, in four steps, so that a failure before the first write leaves
// the directory exactly as it was and every individual file is either its
// previous content or its new content, never a partial write.
//
//  1. Resolve and authorize everything; write nothing. The plan's output
//     directory is created when missing (os.MkdirAll, 0o700) and then
//     opened once, and every later step acts through that one open
//     directory rather than through its path. When the plan recorded a
//     directory -- that is, whenever Dir already existed at Plan time --
//     the open directory must be that same directory, or Commit refuses:
//     a Dir replaced by a symbolic link to somewhere else would otherwise
//     have this plan's deletions applied to files it never read. Every
//     planned file's destination is then re-resolved through
//     genfile.ResolveDestination, and no two of them may resolve to one
//     destination, nor may any of them resolve onto a file step 3 deletes.
//     Whether two destinations are one file is put to the filesystem, not
//     decided by comparing the two paths as strings: a filesystem that
//     ignores case in file names holds a single file for a_gen.go and
//     A_gen.go, and neither resolving nor planning folds the two spellings
//     together. Finally every recorded orphan is re-read, through the open
//     directory, to confirm it is still a regular file carrying rasqlgen's
//     marker on its first line. All of it is checked fresh here rather
//     than trusted from Plan time, since a Plan can be held and acted on
//     later, after the directory has changed underneath it. When Prune is
//     false and there is at least one orphan, Commit refuses here, naming
//     every one of them.
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
	dir, err := os.OpenRoot(p.dir)
	if err != nil {
		return fmt.Errorf("generate: open %s: %w", p.dir, err)
	}
	defer func() { _ = dir.Close() }()
	dirInfo, err := dir.Stat(".")
	if err != nil {
		return fmt.Errorf("generate: check %s: %w", p.dir, err)
	}
	// A plan built when Dir did not exist yet has no directory to compare
	// against and, for the same reason, no orphans: findOrphans reports
	// none for a directory it could not list. There is nothing this plan
	// deletes, so there is nothing an unexpected directory could redirect.
	if p.dirInfo != nil && !os.SameFile(p.dirInfo, dirInfo) {
		return fmt.Errorf("generate: refusing to commit into %s: it is no longer the directory Store.Plan read, so this plan would write and delete in a directory it never looked at; rerun Store.Plan", p.dir)
	}

	// deletions pairs every recorded orphan with the destination a planned
	// file has to resolve to in order to land on it, so the loop below can
	// refuse such a file. That destination is spelled the way a resolved
	// destination is spelled -- the directory as the filesystem reaches it,
	// joined with the file's own name -- because that is the form the two
	// are compared in. Every orphan is a direct child of the directory just
	// authorized, which is what makes its base name enough to rebuild it.
	realDir, err := filepath.EvalSymlinks(p.dir)
	if err != nil {
		return fmt.Errorf("generate: resolve %s: %w", p.dir, err)
	}
	deletions := make([]plannedDeletion, 0, len(p.orphans))
	for _, orphan := range p.orphans {
		deletions = append(deletions, plannedDeletion{orphan: orphan, destination: filepath.Join(realDir, filepath.Base(orphan))})
	}

	// writes holds every destination resolved here, at commit time, rather
	// than discarding each one as soon as it resolves. Two planned files
	// that land in one file, or a planned file that lands on a file step 3
	// deletes, are both invisible to Store.Plan's own check once a symbolic
	// link appears after it ran -- and the second one is not a stale write
	// but a lost file: step 2 writes the planned bytes into the orphan and
	// step 3 then deletes it, leaving neither file on disk and Commit
	// reporting success.
	//
	// Both comparisons go through sameDestination rather than through map
	// lookups on the resolved path, because two destinations that name one
	// file are not always spelled alike: a case-insensitive filesystem
	// holds a single entry for a_gen.go and A_gen.go, and a resolved path
	// keeps whichever of the two the caller spelled.
	writes := make([]plannedWrite, 0, len(p.files))
	for _, f := range p.files {
		resolved, err := genfile.ResolveDestination(f.Path)
		if err != nil {
			return err
		}
		for _, w := range writes {
			if !sameDestination(w.destination, resolved) {
				continue
			}
			return fmt.Errorf("generate: refusing to commit: %s and %s both resolve to %s, so one would overwrite the other; rerun Store.Plan", w.path, f.Path, oneDestination(resolved, w.destination))
		}
		for _, d := range deletions {
			if !sameDestination(d.destination, resolved) {
				continue
			}
			return fmt.Errorf("generate: refusing to commit: %s resolves to %s, which this run deletes as a leftover, so the bytes written there would be deleted again; rerun Store.Plan", f.Path, oneDestination(resolved, d.orphan))
		}
		writes = append(writes, plannedWrite{path: f.Path, destination: resolved})
	}
	for _, orphan := range p.orphans {
		marker, err := hasGenfileMarker(dir, filepath.Base(orphan))
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
			if err := removeGeneratedFile(dir, filepath.Base(orphan)); err != nil {
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

// plannedWrite is one planned file paired with the destination its path
// resolved to, kept so a second file that lands on that same destination can
// be refused naming the first.
type plannedWrite struct {
	path        string
	destination string
}

// plannedDeletion is one recorded orphan paired with the destination a
// planned file has to resolve to in order to land on it.
type plannedDeletion struct {
	orphan      string
	destination string
}

// sameDestination reports whether two resolved destinations name one file.
//
// String equality is not enough. A filesystem that ignores case in file
// names -- macOS's default APFS, and NTFS -- holds a single entry for
// a_gen.go and A_gen.go, and nothing along the way folds the two spellings
// together: filepath.EvalSymlinks does not canonicalize case, and
// genfile.ResolveDestination keeps whichever spelling the caller used. Two
// spellings that fold together are therefore put to the filesystem itself,
// with os.SameFile, so a case-sensitive filesystem -- where a_gen.go and
// A_gen.go really are two files -- keeps them apart. A destination that does
// not exist yet cannot be compared this way and is only ever equal to its
// own spelling; Store.Plan's own file-name check is what refuses a pair of
// planned names that fold together before either exists.
//
// Two names that do not fold together are never one destination here even
// when they share an inode. A hard link is a second name this package may
// write or delete on its own terms, and genfile.Write renames over one name
// rather than writing through it, which leaves the other name holding its
// own bytes.
func sameDestination(a, b string) bool {
	if a == b {
		return true
	}
	if !strings.EqualFold(a, b) {
		return false
	}
	aInfo, err := os.Lstat(a)
	if err != nil {
		return false
	}
	bInfo, err := os.Lstat(b)
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

// ownsEntry reports whether the entry named name in root, whose own Lstat
// result is info, is one of the planned file names in own.
//
// It is sameDestination's rule applied inside an already-open directory: the
// same name, or a name that folds together with a planned one and that the
// filesystem confirms is this very entry. Everything it reads goes through
// root rather than through a path, for the reason findOrphans opens the
// directory once to begin with -- a path is resolved afresh on every call,
// so two reads of the same string are not promised to describe one file.
func ownsEntry(root *os.Root, own map[string]struct{}, name string, info fs.FileInfo) bool {
	if _, exists := own[name]; exists {
		return true
	}
	for planned := range own {
		if !strings.EqualFold(planned, name) {
			continue
		}
		plannedInfo, err := root.Lstat(planned)
		if err != nil {
			continue
		}
		if os.SameFile(plannedInfo, info) {
			return true
		}
	}
	return false
}

// oneDestination describes, for an error message, the single destination two
// spellings name: the spelling itself when both name it the same way, and
// both spellings when they differ, which is what a filesystem that ignores
// case in file names produces for two names differing only in case.
func oneDestination(resolved, recorded string) string {
	if resolved == recorded {
		return recorded
	}
	return fmt.Sprintf("%s, which is the same file as %s", resolved, recorded)
}

// findOrphans reports every leftover file in dir: a regular file directly in
// dir, whose name ends in _gen.go or _gen_test.go, whose first line is
// exactly genfile.Marker, and which is not one of planned's own files. An
// entry a case-insensitive filesystem holds for a planned name spelled
// differently is that planned file rather than a leftover, so it is not
// reported here and not deleted; see ownsEntry. A missing dir reports no
// orphans, matching Store.Plan's promise not to create anything.
//
// It also reports what the filesystem said dir itself was, so Plan.Commit
// can require that the same path still reaches the same directory before it
// deletes anything found here; that is nil exactly when dir was missing and
// no orphans were found. The directory is opened once and everything below
// is read through that one handle, so the listing, each entry's mode and
// each first line all describe the same directory rather than whatever the
// path reaches at each separate call.
//
// An entry that is not a regular file is skipped rather than refused: it is
// not a file this package wrote, so it is not this package's to delete, and
// it is not opened either -- os.Root.Open on a fifo would block until
// something opened the other end.
func findOrphans(dir string, planned []File) ([]string, fs.FileInfo, error) {
	// OpenRoot refuses anything that is not a directory, and does so
	// without opening it for reading, so a dir that is a fifo or a plain
	// file reports an error here rather than blocking or being listed.
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()

	dirInfo, err := root.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, nil, err
	}
	// own holds each planned file's own name rather than its path: every
	// planned path is a direct child of dir, so the name is what
	// distinguishes them, and a name is also what ownsEntry can hand back to
	// the open directory to compare two entries by identity.
	own := make(map[string]struct{}, len(planned))
	for _, f := range planned {
		own[filepath.Base(f.Path)] = struct{}{}
	}

	orphans := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if ownsEntry(root, own, name, info) {
			continue
		}
		marker, err := hasGenfileMarker(root, name)
		if err != nil {
			return nil, nil, err
		}
		if !marker {
			continue
		}
		orphans = append(orphans, filepath.Join(dir, name))
	}
	sort.Strings(orphans)
	return orphans, dirInfo, nil
}

// hasGenfileMarker reports whether name, a file directly inside dir, opens
// with genfile.Marker standing alone on its first line, the same test
// internal/genfile applies before it will ever overwrite an existing
// destination. It is duplicated here, rather than exported from
// internal/genfile, because internal/genfile's own check is a side effect of
// resolving a destination to write, and this call must never write anything.
//
// It takes an open directory and a bare name rather than a path so the file
// it reads is the one inside the directory the caller already has, not
// whatever the path reaches when the kernel resolves it again.
//
// The mode is checked before the file is opened, matching what
// genfile.ResolveDestination does through requireMarker and for the same
// reason: a file that is not a regular file cannot carry a first line to
// begin with, and opening a fifo would block here until something opened the
// other end, with no timeout, in a generator a build or CI run is waiting
// on. Anything that is not a regular file is an error rather than a plain
// "no marker", because the caller recorded a regular file and something has
// since replaced it.
func hasGenfileMarker(dir *os.Root, name string) (bool, error) {
	info, err := dir.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", name)
	}

	file, err := dir.Open(name)
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
