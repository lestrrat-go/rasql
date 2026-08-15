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
// Both take an open directory handle and a bare file name rather than a
// path, so the write and the delete land in the directory Commit already
// authorized. A path would be resolved again by the kernel, component by
// component, and a directory swapped for a symbolic link between the
// authorization and the syscall that follows it would send that write or
// delete somewhere else.
var (
	writeGeneratedFile  = genfile.WriteInto
	removeGeneratedFile = func(dir *os.Root, name string) error { return dir.Remove(name) }
)

// resolveCommitDestination is the same kind of unexported seam, for the one
// step of Commit that authorizes a destination: it resolves a planned
// file's path and, in the same step, records what the filesystem says the
// directory holding that destination is. A test swaps it to change the
// directory at exactly that point; nothing outside this package can reach
// it.
var resolveCommitDestination = resolveDestinationInDirectory

// resolveDestinationInDirectory resolves a planned file's destination and
// reports the directory holding it along with what the filesystem said that
// directory was.
//
// The identity is taken here, beside the resolution that authorized the
// destination, rather than later where the directory is opened. It is what
// binds the two together: the suffix rule and the marker check
// genfile.ResolveDestination applies go through a path, and a path is
// resolved afresh by the kernel on every call, so nothing about the
// directory those checks ran in carries over to the open on its own. A
// destination reached through a symbolic link out of Dir has no other
// record of the directory it landed in -- the plan's own anchor walk covers
// Dir and nothing above or beside it -- so without this the directory
// opened for the write is whatever holds that path by then, which is not
// promised to be the directory this commit resolved and authorized.
func resolveDestinationInDirectory(path string) (string, fs.FileInfo, error) {
	resolved, err := genfile.ResolveDestination(path)
	if err != nil {
		return "", nil, err
	}
	parent := filepath.Dir(resolved)
	info, err := os.Stat(parent)
	if err != nil {
		return "", nil, fmt.Errorf("generate: check %s: %w", parent, err)
	}
	return resolved, info, nil
}

// Plan is a rendered, uncommitted store package: every file's final bytes,
// every destination already resolved, and every leftover already
// identified.
//
// A Plan is a decision made at one instant. It reads the output directory
// once, when Store.Plan builds it. A file that appears in the directory
// afterwards is not reflected here; call Store.Plan again to see the
// directory as it is now. The directory itself is the one exception: a plan
// records the deepest directory on Dir's own path that already existed, and
// Commit refuses to act when that path no longer names that same directory,
// since every orphan it would delete was recorded relative to it and every
// file it would write lands under it. A Dir that did not exist at all is
// recorded the same way, through the closest existing directory above it.
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
	// anchor is the deepest directory on dir's own path that already
	// existed when Store.Plan looked, and anchorInfo is what the filesystem
	// reported for it: dir itself whenever Dir already existed, and the
	// closest existing directory above dir when Dir did not.
	//
	// Commit reopens anchor, requires os.SameFile to agree it is still that
	// same directory, and walks down to dir from there, creating each
	// missing component and refusing any component that is a symbolic link
	// rather than a directory of its own. So a dir replaced by -- or first
	// created as -- a link to somewhere else is refused, instead of having
	// this plan's writes and deletions applied to whatever the link points
	// at. A path string alone cannot carry that: every component of it is
	// resolved afresh by the kernel on each call, so the same string is a
	// different directory once something along it changes. Recording only
	// an existing dir leaves the missing-dir case with nothing to compare,
	// which is a plan that follows whatever has taken Dir's name.
	anchor     string
	anchorInfo fs.FileInfo
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
	// internal/genfile writes through to. It equals Path for an
	// ordinary destination, and differs when Path itself, or a directory
	// holding it, is a symbolic link. Writing through such a link is
	// deliberate, so a Resolved outside the store's Dir is reported here
	// rather than refused. It is unique across a plan's files: Store.Plan
	// rejects two files that resolve to one destination, and asks the
	// filesystem whether two spellings are one file rather than comparing
	// the two strings. Two spellings that fold together and name nothing
	// yet are rejected as well, since nothing tells them apart before one
	// of them exists.
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
//     directory is reached by reopening the deepest directory on its path
//     that Store.Plan actually saw, requiring it to still be that same
//     directory, and walking down from there: each missing component below
//     it is created (0o700) and each component that already exists must be
//     a directory of its own rather than a symbolic link that has taken
//     the name. Every later step acts through the one open directory that
//     walk ends at, rather than through its path -- a Dir replaced by, or
//     first created as, a symbolic link to somewhere else would otherwise
//     have this plan's writes and deletions applied to files it never
//     read. Every planned file's destination is then re-resolved through
//     genfile.ResolveDestination, and no two of them may resolve to one
//     destination, nor may any of them resolve onto a file step 3 deletes.
//     Whether two destinations are one file is put to the filesystem, not
//     decided by comparing the two paths as strings: a filesystem that
//     ignores case in file names holds a single file for a_gen.go and
//     A_gen.go, and neither resolving nor planning folds the two spellings
//     together. Two spellings that fold together and that the filesystem
//     cannot answer for, because neither of them exists yet, are refused as
//     one destination rather than allowed as two. The directory holding each resolved destination is opened
//     here too, so steps 2 and 4 write through a directory this step
//     authorized instead of resolving that path a second time, and that
//     open directory must be the very directory the destination resolved
//     in -- otherwise a destination reached through a symbolic link out of
//     Dir is authorized in one directory and written in whichever one has
//     since taken that path. Finally
//     every recorded orphan is re-read, through the open directory, to
//     confirm it is still a regular file carrying rasqlgen's marker on its
//     first line. All of it is checked fresh here rather than trusted from
//     Plan time, since a Plan can be held and acted on later, after the
//     directory has changed underneath it. When Prune is false and there
//     is at least one orphan, Commit refuses here, naming every one of
//     them.
//  2. Write every per-table file and every query file, in path order.
//  3. Delete this run's leftovers: every path Orphans reported, in path
//     order, and only when Prune is set -- otherwise step 1 already
//     refused the run. Each one is re-read immediately before it is
//     deleted and must still be the very file step 1 authorized, marker
//     and identity both, so a file that took its name in between is not
//     deleted on the strength of a check that never saw it.
//  4. Write the aggregator last: schema_gen.go, then schema_gen_test.go.
//     The aggregator is the store's whole record of the schema -- every
//     descriptor literal and Tables() -- so until this step lands, the
//     record on disk is still the previous run's, complete and internally
//     consistent on its own.
//
// If the process dies partway through, every file on disk is either its
// previous content or its new content -- internal/genfile never truncates a
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
	dir, err := openPlannedDirectory(p.anchor, p.anchorInfo, p.dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	// realDir is the plan's own output directory as the filesystem reaches
	// it, which is the form every resolved destination below is spelled in.
	// It is put back to the handle just authorized, with os.SameFile,
	// because it comes from resolving a path a second time and a second
	// resolution of one string is not promised to reach the same directory
	// the first did.
	realDir, err := filepath.EvalSymlinks(p.dir)
	if err != nil {
		return fmt.Errorf("generate: resolve %s: %w", p.dir, err)
	}
	dirInfo, err := dir.Stat(".")
	if err != nil {
		return fmt.Errorf("generate: check %s: %w", p.dir, err)
	}
	realDirInfo, err := os.Stat(realDir)
	if err != nil {
		return fmt.Errorf("generate: check %s: %w", realDir, err)
	}
	if !os.SameFile(dirInfo, realDirInfo) {
		return fmt.Errorf("generate: refusing to commit into %s: it is no longer the directory this commit authorized; rerun Store.Plan", p.dir)
	}

	// deletions pairs every recorded orphan with the destination a planned
	// file has to resolve to in order to land on it, so the loop below can
	// refuse such a file. That destination is spelled the way a resolved
	// destination is spelled -- the directory as the filesystem reaches it,
	// joined with the file's own name -- because that is the form the two
	// are compared in. Every orphan is a direct child of the directory just
	// authorized, which is what makes its base name enough to rebuild it.
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
	// Both comparisons go through matchDestinations rather than through map
	// lookups on the resolved path, because two destinations that name one
	// file are not always spelled alike: a case-insensitive filesystem
	// holds a single entry for a_gen.go and A_gen.go, and a resolved path
	// keeps whichever of the two the caller spelled. A pair that folds
	// together and that nothing on disk tells apart -- two destinations
	// neither of which exists yet -- counts as one destination here, since
	// there is no answer to be had before the first of the two writes
	// creates it.
	//
	// The directory holding each destination is opened here as well, and
	// steps 2 and 4 write through it. Handing the planned path to the
	// writer instead would have it resolve that path a second time, and
	// the destination the second resolution reaches is not promised to be
	// the one authorized here.
	//
	// That open directory is required to be the one the resolution itself
	// saw, which is why resolveCommitDestination reports the directory's
	// identity beside the destination. Opening the resolved parent by path
	// is a second resolution of its own, and for a destination a symbolic
	// link reaches out of Dir it is the only one: the anchor walk pins Dir
	// and nothing beyond it, so without the comparison a directory
	// replaced between the two lands this commit's bytes somewhere it
	// never looked.
	handles := destinationDirectories{own: dir, ownPath: realDir}
	defer handles.close()
	writes := make([]commitWrite, 0, len(p.files))
	for i := range p.files {
		f := &p.files[i]
		resolved, parentInfo, err := resolveCommitDestination(f.Path)
		if err != nil {
			return err
		}
		for _, w := range writes {
			match := matchDestinations(w.destination, resolved)
			if match == distinctDestinations {
				continue
			}
			return fmt.Errorf("generate: refusing to commit: %s and %s both resolve to %s, so one would overwrite the other; rerun Store.Plan", w.file.Path, f.Path, match.describe(resolved, w.destination))
		}
		for _, d := range deletions {
			match := matchDestinations(d.destination, resolved)
			if match == distinctDestinations {
				continue
			}
			return fmt.Errorf("generate: refusing to commit: %s resolves to %s, which this run deletes as a leftover, so the bytes written there would be deleted again; rerun Store.Plan", f.Path, match.describe(resolved, d.orphan))
		}
		into, err := handles.open(filepath.Dir(resolved), parentInfo)
		if err != nil {
			return err
		}
		writes = append(writes, commitWrite{file: f, destination: resolved, dir: into, name: filepath.Base(resolved)})
	}
	orphans := make([]validatedOrphan, 0, len(p.orphans))
	for _, orphan := range p.orphans {
		name := filepath.Base(orphan)
		info, err := readGenfileMarker(dir, name)
		if err != nil {
			return fmt.Errorf("generate: check %s for rasqlgen's marker: %w", orphan, err)
		}
		if info == nil {
			return fmt.Errorf("generate: refusing to delete %s: it no longer opens with rasqlgen's marker; something has changed it since Store.Plan ran", orphan)
		}
		orphans = append(orphans, validatedOrphan{orphan: orphan, name: name, info: info})
	}
	if !p.prune && len(p.orphans) > 0 {
		return fmt.Errorf("generate: %s holds %d file(s) rasqlgen wrote that this plan does not write, and Store.Prune is false: %s; set Prune to delete them, or remove them yourself", p.dir, len(p.orphans), strings.Join(p.orphans, ", "))
	}

	// The aggregator files are singled out here, once, so steps 2 and 4
	// below can write in the order Commit's own doc comment promises:
	// every per-table and query file first, the aggregator last. p.files
	// is already sorted by Path (Store.Plan sorts it), and writes was built
	// in that order, so it is preserved within each group below.
	var descriptor, descriptorTest *commitWrite
	rest := make([]commitWrite, 0, len(writes))
	for i := range writes {
		switch filepath.Base(writes[i].file.Path) {
		case schemaDescriptorFilename:
			descriptor = &writes[i]
		case schemaDescriptorTestFilename:
			descriptorTest = &writes[i]
		default:
			rest = append(rest, writes[i])
		}
	}
	if descriptor == nil || descriptorTest == nil {
		return fmt.Errorf("generate: internal error: plan for %s is missing its own %s or %s", p.dir, schemaDescriptorFilename, schemaDescriptorTestFilename)
	}

	// Step 2: write every per-table file and every query file.
	for _, w := range rest {
		if err := writeGeneratedFile(w.dir, w.name, w.file.Source); err != nil {
			return fmt.Errorf("generate: write %s: %w", w.file.Path, err)
		}
	}

	// Step 3: delete this run's leftovers, only when Prune is set --
	// otherwise step 1 already refused the run above.
	if p.prune {
		for _, orphan := range orphans {
			if err := orphan.confirm(dir); err != nil {
				return err
			}
			if err := removeGeneratedFile(dir, orphan.name); err != nil {
				return fmt.Errorf("generate: delete %s: %w", orphan.orphan, err)
			}
		}
	}

	// Step 4: write the aggregator last.
	if err := writeGeneratedFile(descriptor.dir, descriptor.name, descriptor.file.Source); err != nil {
		return fmt.Errorf("generate: write %s: %w", descriptor.file.Path, err)
	}
	if err := writeGeneratedFile(descriptorTest.dir, descriptorTest.name, descriptorTest.file.Source); err != nil {
		return fmt.Errorf("generate: write %s: %w", descriptorTest.file.Path, err)
	}
	return nil
}

// openPlannedDirectory opens the output directory this plan was built for,
// reaching it the way the plan recorded it rather than by resolving its
// path afresh.
//
// anchor is the deepest directory on dir's path that existed when
// Store.Plan looked and anchorInfo what the filesystem said it was, so the
// walk starts somewhere whose identity can be checked. Every component
// below it is created when missing and opened through the directory above
// it, and a component that is a symbolic link is refused rather than
// followed: a Dir that appeared as a link to an unrelated directory after
// the plan was built would otherwise take every planned write with it, and
// os.MkdirAll and os.OpenRoot on the plan's own path both follow such a
// link without noticing.
func openPlannedDirectory(anchor string, anchorInfo fs.FileInfo, dir string) (*os.Root, error) {
	if anchor == "" || anchorInfo == nil {
		return nil, fmt.Errorf("generate: internal error: plan for %s recorded no directory to commit through; rerun Store.Plan", dir)
	}
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, fmt.Errorf("generate: open %s: %w", anchor, err)
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("generate: check %s: %w", anchor, err)
	}
	if !os.SameFile(anchorInfo, info) {
		_ = root.Close()
		return nil, fmt.Errorf("generate: refusing to commit into %s: %s is no longer the directory Store.Plan read, so this plan would write and delete in a directory it never looked at; rerun Store.Plan", dir, anchor)
	}
	rel, err := filepath.Rel(anchor, dir)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("generate: locate %s under %s: %w", dir, anchor, err)
	}
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		// filepath.Rel reports "." when the two are one directory, which
		// is every plan whose Dir already existed.
		if component == "." {
			continue
		}
		child, childErr := openChildDirectory(root, component)
		_ = root.Close()
		if childErr != nil {
			return nil, fmt.Errorf("generate: refusing to commit into %s: %w", dir, childErr)
		}
		root = child
	}
	return root, nil
}

// openChildDirectory opens the directory named name directly inside root,
// creating it when it is missing, and refuses anything that is not a
// directory entry of its own.
//
// A name that is a symbolic link is refused rather than followed, even one
// pointing at a directory inside root: os.Root follows a link that stays
// within its own tree, and what is wanted here is the directory the plan
// was built for, not whatever a link that appeared later names. The handle
// is then checked, with os.SameFile, against what the name was just seen to
// be, so a name replaced between the two calls is refused as well.
func openChildDirectory(root *os.Root, name string) (*os.Root, error) {
	if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", name, err)
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", name, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link rather than the directory this plan was built for; rerun Store.Plan", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", name)
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	childInfo, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, fmt.Errorf("check %s: %w", name, err)
	}
	if !os.SameFile(info, childInfo) {
		_ = child.Close()
		return nil, fmt.Errorf("%s is no longer the directory it was a moment ago; rerun Store.Plan", name)
	}
	return child, nil
}

// deepestExistingDirectory reports the closest directory above dir that
// already exists, and what the filesystem said it was.
//
// A plan for a Dir that does not exist yet has no directory of its own to
// record, so it records this one: Commit reopens it, requires it to still
// be the same directory, and creates the rest of the way down through it.
// Without that there is nothing to compare a missing Dir against, and a Dir
// that appears as a symbolic link between Plan and Commit is followed to
// wherever it points, with every planned file written there.
func deepestExistingDirectory(dir string) (string, fs.FileInfo, error) {
	current := filepath.Dir(dir)
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", nil, fmt.Errorf("%s is not a directory", current)
			}
			return current, info, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no directory above %s exists", dir)
		}
		current = parent
	}
}

// destinationDirectories hands out an open handle on the directory holding
// each resolved destination, so steps 2 and 4 write through a directory
// Commit authorized instead of resolving the destination's path a second
// time.
//
// The plan's own output directory is the common case and is already open,
// so it is reused rather than opened again, and closing it stays with
// whoever opened it. A destination outside it -- which a symbolic link
// inside Dir reaches on purpose -- gets its own handle, opened once per
// directory and closed when the commit is over.
//
// Every handle handed out, the plan's own directory included, is required
// to be the directory the destination's own resolution saw. Without that an
// outside destination is authorized through one directory and written
// through another: os.OpenRoot resolves each component of the path afresh,
// so a directory renamed out of the way after the resolution and replaced
// by a second one leaves the write landing in a directory this commit never
// resolved. The plan's own directory is pinned twice over -- by the anchor
// walk and by Commit's own os.SameFile against the resolved Dir -- and the
// check costs it nothing, so it is applied uniformly rather than only where
// it is the sole binding.
type destinationDirectories struct {
	own     *os.Root
	ownPath string
	outside map[string]*os.Root
}

// open reports the open directory holding a resolved destination. resolved
// is what the filesystem said that directory was when the destination was
// resolved, and the handle open reports is required to still be that same
// directory.
func (d *destinationDirectories) open(path string, resolved fs.FileInfo) (*os.Root, error) {
	if path == d.ownPath {
		if err := requireSameDirectory(d.own, path, resolved); err != nil {
			return nil, err
		}
		return d.own, nil
	}
	if root, exists := d.outside[path]; exists {
		// A handle already opened for an earlier destination is checked
		// again rather than trusted: this destination was resolved through
		// its own look at the path, and only this comparison says the two
		// looks found one directory.
		if err := requireSameDirectory(root, path, resolved); err != nil {
			return nil, err
		}
		return root, nil
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("generate: open %s: %w", path, err)
	}
	if err := requireSameDirectory(root, path, resolved); err != nil {
		_ = root.Close()
		return nil, err
	}
	if d.outside == nil {
		d.outside = make(map[string]*os.Root, 1)
	}
	d.outside[path] = root
	return root, nil
}

// requireSameDirectory reports an error unless root is the very directory
// resolved describes. It asks the open handle what it is rather than the
// path again, which is the whole point: the path is what stopped being
// trustworthy.
func requireSameDirectory(root *os.Root, path string, resolved fs.FileInfo) error {
	info, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("generate: check %s: %w", path, err)
	}
	if !os.SameFile(resolved, info) {
		return fmt.Errorf("generate: refusing to commit into %s: it is no longer the directory this commit resolved the destination through, so the generated bytes would land in a directory nothing authorized; rerun Store.Plan", path)
	}
	return nil
}

func (d *destinationDirectories) close() {
	for _, root := range d.outside {
		_ = root.Close()
	}
}

// commitWrite is one planned file bound to the destination Commit
// authorized for it: the directory holding that destination, already open,
// and the file's own name inside it.
type commitWrite struct {
	// file is the planned file itself, whose Path names the destination as
	// planned and whose Source is the bytes to write.
	file *File
	// destination is where Path resolved to at commit time, kept for the
	// comparisons that refuse two files landing on one destination.
	destination string
	// dir holds destination, and name is destination's own name inside it.
	dir  *os.Root
	name string
}

// validatedOrphan is one recorded orphan that step 1 confirmed is still a
// regular file carrying rasqlgen's marker, together with what the
// filesystem said that file was.
//
// The identity is kept because os.Root pins the directory, not the entry
// inside it: the marker check and the deletion both go through one open
// directory, so both act in the directory Commit authorized, but each
// resolves the bare name afresh there. Nothing about the checked file
// carries over on its own, so a file renamed over that name in between is
// deleted on the strength of a check that never looked at it.
type validatedOrphan struct {
	// orphan is the path Store.Plan recorded, which is what an error names.
	orphan string
	// name is the orphan's own name inside the authorized directory, which
	// every read and the deletion itself go through.
	name string
	// info is what the filesystem said the checked file was, taken from the
	// open file rather than from a second look at the name.
	info fs.FileInfo
}

// confirm re-reads the orphan through dir and reports an error unless the
// name still holds the very file step 1 authorized, marker and identity
// both.
//
// It runs immediately before each deletion rather than once for the whole
// run, so what a deletion acts on is what was checked. What is left is the
// gap the filesystem offers no way to close: there is no way to delete a
// directory entry by identity, so a name changed between this check and the
// unlink that follows it is still unlinked. Closing that needs the commit
// serialized against every other writer, which is a lock this package does
// not own.
func (o validatedOrphan) confirm(dir *os.Root) error {
	info, err := readGenfileMarker(dir, o.name)
	if err != nil {
		return fmt.Errorf("generate: check %s for rasqlgen's marker: %w", o.orphan, err)
	}
	if info == nil {
		return fmt.Errorf("generate: refusing to delete %s: it no longer opens with rasqlgen's marker; something has changed it since Store.Plan ran", o.orphan)
	}
	if !os.SameFile(o.info, info) {
		return fmt.Errorf("generate: refusing to delete %s: it is no longer the file this commit checked, so deleting it would delete a file nothing authorized; rerun Store.Plan", o.orphan)
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

// destinationMatch is what comparing two resolved destinations concluded:
// two files, one file, or a pair nothing on disk tells apart.
type destinationMatch int

const (
	// distinctDestinations is two files: two spellings that do not fold
	// together, or two the filesystem itself reported as separate files.
	distinctDestinations destinationMatch = iota
	// oneDestination is one file: one spelling used twice, or two the
	// filesystem confirmed are a single entry.
	oneDestination
	// undecidedDestinations is a pair that folds together and that nothing
	// on disk answers for, because neither spelling names anything yet or
	// because the filesystem could not be asked. Every caller treats it as
	// one destination; see matchDestinations for why.
	undecidedDestinations
)

// matchDestinations reports whether two resolved destinations name one file.
//
// String equality is not enough. A filesystem that ignores case in file
// names -- macOS's default APFS, and NTFS -- holds a single entry for
// a_gen.go and A_gen.go, and nothing along the way folds the two spellings
// together: filepath.EvalSymlinks does not canonicalize case, and
// genfile.ResolveDestination keeps whichever spelling the caller used. Two
// spellings that fold together are therefore put to the filesystem itself,
// with os.SameFile, so a case-sensitive filesystem -- where a_gen.go and
// A_gen.go really are two files -- keeps them apart.
//
// The filesystem only answers for a destination that exists. One spelling
// naming an existing file while the other names nothing is an answer all the
// same, and it is "two files": a filesystem that ignored case would have
// found that one entry under both spellings. A pair where neither spelling
// exists has no answer at all, and is reported undecided rather than
// distinct, because reporting it distinct is a commit that writes both
// spellings into the one entry a case-insensitive filesystem holds, deletes
// nothing, and reports success with one planned file's bytes gone. A pair the
// filesystem could not be asked about, because a look at either spelling
// failed for any other reason, is reported the same conservative way.
//
// Refusing an undecided pair costs a case-sensitive filesystem, where the two
// really are two files, the unusual plan that reaches two not-yet-existing
// destinations spelled apart only by case through symbolic links. Store.Plan
// already makes that trade for the names inside Dir, which filenameKey folds
// on every platform for the same reason: one Store plans the same package
// wherever it runs.
//
// Two names that do not fold together are never one destination here even
// when they share an inode. A hard link is a second name this package may
// write or delete on its own terms, and internal/genfile renames over one name
// rather than writing through it, which leaves the other name holding its
// own bytes.
//
// What stays open is the window between the two looks: a spelling created or
// removed in between is answered for a pair that no longer holds. Closing
// that needs the commit serialized against every other writer, which is a
// lock this package does not own.
func matchDestinations(a, b string) destinationMatch {
	if a == b {
		return oneDestination
	}
	if !strings.EqualFold(a, b) {
		return distinctDestinations
	}
	aInfo, aErr := os.Lstat(a)
	bInfo, bErr := os.Lstat(b)
	if aErr == nil && bErr == nil {
		if os.SameFile(aInfo, bInfo) {
			return oneDestination
		}
		return distinctDestinations
	}
	if aErr == nil && errors.Is(bErr, fs.ErrNotExist) {
		return distinctDestinations
	}
	if bErr == nil && errors.Is(aErr, fs.ErrNotExist) {
		return distinctDestinations
	}
	return undecidedDestinations
}

// ownsEntry reports whether the entry named name in root, whose own Lstat
// result is info, is one of the planned file names in own.
//
// It is matchDestinations' question asked inside an already-open directory,
// where the filesystem can always answer it: the same name, or a name that
// folds together with a planned one and that the filesystem confirms is this
// very entry. There is no undecided pair here, because name is an entry that
// exists -- a planned name that folds together with it and yet names nothing
// in this directory is this filesystem telling the two spellings apart, which
// makes the entry a leftover rather than the planned file. Everything it reads goes through
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

// describe names, for an error message, the single destination two spellings
// are being treated as: the spelling itself when both name it the same way,
// both spellings when the filesystem reported one file for them, and both
// with what is not known about them when nothing on disk tells them apart.
func (m destinationMatch) describe(resolved, recorded string) string {
	if resolved == recorded {
		return recorded
	}
	if m == undecidedDestinations {
		return fmt.Sprintf("%s, which nothing on disk tells apart from %s: a filesystem that ignores case in file names holds one entry for the two", resolved, recorded)
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
// It also reports what the filesystem said dir itself was, which is what
// Store.Plan records as the plan's anchor: Plan.Commit reopens it and
// requires it to still be the same directory before it writes or deletes
// anything found here. That is nil exactly when dir was missing and no
// orphans were found, and Store.Plan then anchors the plan to the closest
// existing directory above dir instead; see deepestExistingDirectory.
// The directory is opened once and everything below
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
		marker, err := readGenfileMarker(root, name)
		if err != nil {
			return nil, nil, err
		}
		if marker == nil {
			continue
		}
		orphans = append(orphans, filepath.Join(dir, name))
	}
	sort.Strings(orphans)
	return orphans, dirInfo, nil
}

// readGenfileMarker reports what the filesystem says the file named name,
// directly inside dir, is -- but only when that file opens with
// genfile.Marker standing alone on its first line, the same test
// internal/genfile applies before it will ever overwrite an existing
// destination. A file that does not carry the marker, and a name nothing
// holds, are both reported as a nil FileInfo and no error. The check is
// duplicated here, rather than exported from internal/genfile, because
// internal/genfile's own check is a side effect of resolving a destination
// to write, and this call must never write anything.
//
// It takes an open directory and a bare name rather than a path so the file
// it reads is the one inside the directory the caller already has, not
// whatever the path reaches when the kernel resolves it again.
//
// What it reports comes from the open file rather than from a second look
// at the name, so it describes the very bytes the marker was read from. A
// caller that acts on the answer later hands it to os.SameFile to require
// that the name still holds that same file, since os.Root pins the
// directory and not the entry in it.
//
// The mode is checked before the file is opened, matching what
// genfile.ResolveDestination does through requireMarker and for the same
// reason: a file that is not a regular file cannot carry a first line to
// begin with, and opening a fifo would block here until something opened the
// other end, with no timeout, in a generator a build or CI run is waiting
// on. Anything that is not a regular file is an error rather than a plain
// "no marker", because the caller recorded a regular file and something has
// since replaced it.
func readGenfileMarker(dir *os.Root, name string) (fs.FileInfo, error) {
	info, err := dir.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}

	file, err := dir.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	// The open file is asked what it is a second time, because the Lstat
	// above described whatever held the name at that instant and the open
	// resolved the name again. This is the answer that belongs to the bytes
	// read below.
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}

	head := make([]byte, len(genfile.Marker)+1)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	head = head[:n]
	if !bytes.HasPrefix(head, []byte(genfile.Marker)) {
		return nil, nil
	}
	rest := head[len(genfile.Marker):]
	if len(rest) == 0 || rest[0] == '\n' || rest[0] == '\r' {
		return opened, nil
	}
	return nil, nil
}
