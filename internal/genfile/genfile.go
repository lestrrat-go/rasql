// Package genfile writes generated Go source without ever truncating an
// existing file in place. It is internal because both generate and
// cli/rasqlgen need it and no caller outside this module does.
//
// Every write lands by renaming a complete temporary file over the
// destination, and that rename is atomic only on Unix: os.Rename documents
// that even within a single directory it is not an atomic operation on
// non-Unix platforms, so a run interrupted at that rename can leave the
// destination missing or still holding its old contents. Write and
// WriteInto publish through the same rename and name this limit rather
// than restating it; generate.Plan.Commit states what it costs a whole
// commit. Nothing in this package makes the limit go away, and the tests
// that pin the replacement's behavior are guarded by //go:build unix.
package genfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Write writes source to path without ever truncating an
// existing file in place. Its resolved destination must end in _gen.go, or
// in _gen_test.go for a generated test file, and, when it already exists,
// must be a file rasqlgen itself wrote; ResolveDestination owns both rules,
// and the write re-reads the destination's own first line through the
// directory holding it before it replaces anything.
// When path is a symbolic link it writes through
// the link to the file the link points at, leaving the link itself intact.
// A path that names a directory, or a symbolic link that resolves to one,
// is rejected with an error instead of being written into, regardless of
// whether path ends in a trailing slash.
// It writes to a temporary file in the same directory as that resolved
// destination, then renames the temporary file over the destination only
// after the write fully succeeds, so a failure at any point along the way
// leaves the destination untouched instead of empty or partially written.
// When the destination already exists, the mode bits it already carries are
// copied to the temporary file before the rename, so regenerating never
// changes the mode of an existing output file; a file that did not exist is
// created at 0600. existingDestinationMode owns which bits travel.
//
// The rename that publishes the file is atomic only on Unix; the package
// comment owns that limit and what an interruption elsewhere can leave
// behind.
func Write(path string, source []byte) error {
	// Both the temporary file's directory and the rename destination come
	// from the resolved path. Resolving only the destination would put the
	// temporary file beside the link instead of beside its target, and
	// renaming across filesystems fails with EXDEV.
	destination, err := ResolveDestination(path)
	if err != nil {
		return err
	}
	dir, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return writeInto(dir, filepath.Base(destination), destination, source)
}

// WriteInto writes source to name, a file directly inside the already-open
// directory dir, and promises everything Write promises: the destination is
// never truncated in place, an existing destination must be a regular file
// carrying Marker on its first line, and the mode bits an existing
// destination has survive the write.
//
// It adds the one guarantee a path cannot give. Every step -- reading the
// destination's first line, creating the temporary file, the rename -- goes
// through the directory handle the caller already holds, so a directory
// along the way that is replaced after the caller authorized the write
// cannot send any of it somewhere else. A path is resolved afresh by the
// kernel on every call, which is what lets one authorized destination and
// the write that follows it land in two different directories.
//
// name is a bare file name and is not resolved. A caller that means to
// follow a symbolic link resolves it with ResolveDestination first and
// passes the destination that reports, together with the directory holding
// it; an entry that is a symbolic link by the time this runs is refused
// rather than followed, because it is no longer the file the caller
// authorized.
//
// The extra guarantee is about which directory the steps land in and not
// about the rename itself, which is the very rename Write publishes
// through: the package comment's Unix-only limit applies here unchanged.
func WriteInto(dir *os.Root, name string, source []byte) error {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return fmt.Errorf("generated output %q must be a file name inside the directory, not a path", name)
	}
	return writeInto(dir, name, filepath.Join(dir.Name(), name), source)
}

// writeInto is the whole write, from an open directory and a bare name.
// display is how the destination is spelled in an error message: the whole
// resolved path when the caller had one, and the directory's own name
// joined with name when it did not.
func writeInto(dir *os.Root, name, display string, source []byte) error {
	if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
		return fmt.Errorf("generated output %q must end in _gen.go", display)
	}
	mode, err := existingDestinationMode(dir, name, display)
	if err != nil {
		return err
	}
	temporary, temporaryName, err := createTemporary(dir, name)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = dir.Remove(temporaryName)
		}
	}()

	if _, err := temporary.Write(source); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// chmod before the rename, so the destination is never visible with
	// the temporary file's mode instead of the mode it had before.
	if err := dir.Chmod(temporaryName, mode); err != nil {
		return err
	}
	if err := dir.Rename(temporaryName, name); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

// temporaryNameAttempts caps how many names createTemporary tries before it
// gives up, the same cap os.CreateTemp applies.
const temporaryNameAttempts = 10000

// createTemporary creates a file directly inside dir under a name nothing
// else holds, and reports it along with the name it was created under. It
// stands in for os.CreateTemp, which takes a directory path rather than an
// open directory: creating the temporary file through the same handle the
// rename uses is what keeps both of them in the directory the caller
// authorized.
//
// The name is random only so two writers rarely collide. It is not a
// secret, and does not need to be: the file is created with O_EXCL inside
// dir, so a name somebody else already holds costs another attempt rather
// than handing this write their file.
func createTemporary(dir *os.Root, name string) (*os.File, string, error) {
	prefix := "." + name + ".tmp"
	for attempt := 0; attempt < temporaryNameAttempts; attempt++ {
		candidate := prefix + strconv.FormatUint(rand.Uint64(), 16)
		file, err := dir.OpenFile(candidate, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, candidate, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("create a temporary file beside %s: %d names were all taken", name, temporaryNameAttempts)
}

// existingDestinationMode reports the mode bits writeInto must give name,
// and refuses a destination rasqlgen may not replace: one that already
// exists and is not a regular file, and one that does not open with Marker
// standing alone on its first line. It is requireMarker's check made
// through an open directory rather than through a path, so the file whose
// first line decides the write is the file the write then lands on.
//
// An existing file keeps the bits it already has, because the generated
// output's mode is the caller's to choose. Only a destination that does not
// exist yet gets 0600.
//
// The sticky bit travels with the permission bits, because writing the file
// in place kept it and replacing the file must not quietly drop it. Setuid
// and setgid deliberately do not travel: Linux clears both when an
// unprivileged process writes a file, so writing in place lost them too,
// and carrying them across here would make regenerating a file stricter
// than writing it directly.
//
// The mode is read before the file is opened, for the reason requireMarker
// reads it first: a file that is not a regular file cannot carry a first
// line to begin with, and opening a fifo would block here until something
// opened the other end.
func existingDestinationMode(dir *os.Root, name, display string) (fs.FileMode, error) {
	info, err := dir.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0o600, nil
		}
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("refusing to overwrite %s: it already exists and is not a regular file", display)
	}

	file, err := dir.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0o600, nil
		}
		return 0, err
	}
	defer func() { _ = file.Close() }()

	// One byte past the marker separates a first line that is the marker
	// from a longer line that merely starts with it.
	head := make([]byte, len(Marker)+1)
	read, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	if !startsWithMarker(head[:read]) {
		return 0, fmt.Errorf("refusing to overwrite %s: it was not generated by rasqlgen, because its first line is not %q", display, Marker)
	}
	return info.Mode() & (fs.ModePerm | fs.ModeSticky), nil
}

// Marker is the first line of every file rasqlgen produces.
// It is the one thing that tells a file rasqlgen may replace apart from a
// file somebody wrote by hand, so nothing in this package replaces a
// destination that already exists without it: ResolveDestination requires
// it of the path it resolves, and the write requires it again of the file
// it is about to rename over.
//
// The generators write this line themselves rather than reading it from
// here, because they are separate packages this one must not reach into.
// TestGeneratedOutputCarriesTheMarker holds the two sides together by
// running both commands and checking the first line of everything they
// write, so a generator that changed its header fails there.
const Marker = "// Code generated by rasqlgen; DO NOT EDIT."

// ResolveDestination reports the file Write must replace for the requested
// output path, and refuses a destination that rasqlgen may not replace.
//
// A destination must end in _gen.go, or in _gen_test.go for a generated
// test file. A destination that does not exist yet is free to be created. A
// destination that does exist must open with Marker as its whole first
// line: without that check a hand-written file sitting where generated
// output lands is destroyed by the rename, silently and with a successful
// exit status, and the only copy of it is gone.
//
// Callers that write several files in one run resolve every destination
// before writing any of them, so a refusal cannot leave a package half
// regenerated.
func ResolveDestination(path string) (string, error) {
	destination, err := resolveOutputPath(path)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(destination, "_gen.go") && !strings.HasSuffix(destination, "_gen_test.go") {
		return "", fmt.Errorf("generated output %q must end in _gen.go", destination)
	}
	if err := requireMarker(destination); err != nil {
		return "", err
	}
	return destination, nil
}

// requireMarker reports whether destination is safe to replace, which it is
// when it does not exist yet or when Marker stands on its first line.
//
// The mode is checked before the file is opened. A destination that is not
// a regular file cannot carry a first line to begin with, and opening a
// fifo would block here until something opened the other end.
func requireMarker(destination string) error {
	info, err := os.Stat(destination)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to overwrite %s: it already exists and is not a regular file", destination)
	}
	file, err := os.Open(destination)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	// One byte past the marker separates a first line that is the marker
	// from a longer line that merely starts with it.
	head := make([]byte, len(Marker)+1)
	read, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	if !startsWithMarker(head[:read]) {
		return fmt.Errorf("refusing to overwrite %s: it was not generated by rasqlgen, because its first line is not %q", destination, Marker)
	}
	return nil
}

// startsWithMarker reports whether head, the first bytes of a file, begin
// with Marker standing alone on its first line. A file holding nothing but
// the marker has no line ending to check, so an exhausted head counts as
// the end of that line.
func startsWithMarker(head []byte) bool {
	if !bytes.HasPrefix(head, []byte(Marker)) {
		return false
	}
	rest := head[len(Marker):]
	return len(rest) == 0 || rest[0] == '\n' || rest[0] == '\r'
}

// maxOutputSymlinkDepth caps how many symbolic links resolveOutputPath
// follows, so a link that points at itself or at another link in a cycle
// reports an error instead of looping forever. Linux allows 40 links in a
// single resolution, so this matches what an ordinary open would accept:
// the destination the fortieth link names is still used, and only a
// forty-first link that must be resolved is too many.
const maxOutputSymlinkDepth = 40

// resolveOutputPath reports the file Write must replace for
// the requested output path. A path that is not a symbolic link is its own
// destination. A symbolic link resolves to whatever it points at, so the
// generated source lands in the file the link names and the link itself
// survives the rename; writing to the link's own path would instead delete
// the link and leave its target holding stale content.
//
// A path that names a directory, or a symbolic link that resolves to one,
// is rejected instead of being treated as a destination. os.Lstat follows
// a trailing slash through to the directory even when the final component
// is a link, so this one check catches a directory named with or without a
// trailing slash and a directory reached through a link either way; without
// it, withResolvedParent would rewrite a path such as "outdir/" into a new
// child file "outdir/outdir" inside that directory and report success on
// what was almost certainly a typo.
//
// A link that points at a path which does not exist resolves to that
// missing path, matching what writing through the link would have done:
// the missing file is created, with the 0600 existingDestinationMode gives
// any new file, and the link keeps pointing at it. filepath.EvalSymlinks
// cannot resolve the whole path here, because it fails on such a dangling
// link; it is used only on directories, which always exist by the time
// they are resolved.
func resolveOutputPath(path string) (string, error) {
	current := path
	// followed counts the links already resolved. Testing it before the
	// next Readlink rather than after the last one keeps the destination
	// the fortieth link names in play, so a chain the kernel would accept
	// is accepted here too.
	for followed := 0; ; followed++ {
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return withResolvedParent(current), nil
			}
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("resolve output %s: is a directory", path)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return withResolvedParent(current), nil
		}
		if followed == maxOutputSymlinkDepth {
			return "", fmt.Errorf("resolve output %s: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			current = target
			continue
		}
		// A relative link is relative to the directory holding the link,
		// and that is the directory the filesystem reaches rather than the
		// one filepath.Dir spells. Joining against the lexical parent
		// collapses a leading ".." in the target against a parent that is
		// itself a link, which lands the resolved path beside the link
		// instead of beside the directory the link points at. The Lstat
		// above succeeded, so that parent exists and resolves.
		parent, err := filepath.EvalSymlinks(filepath.Dir(current))
		if err != nil {
			return "", err
		}
		current = filepath.Join(parent, target)
	}
}

// withResolvedParent reports path with the directory holding it replaced
// by the directory the filesystem reaches, so Write creates
// its temporary file in, and renames over, the directory an ordinary open
// of path would have used. A parent that cannot be resolved is left as it
// was spelled, which leaves the resulting open to report the ENOENT an
// ordinary open would have reported.
//
// path is never itself a directory here: resolveOutputPath rejects that
// case before calling this, so filepath.Base(path) always names the file
// the caller asked for rather than the directory holding it.
func withResolvedParent(path string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(parent, filepath.Base(path))
}
