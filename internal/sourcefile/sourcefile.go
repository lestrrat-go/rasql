// Package sourcefile snapshots a source file so its later re-read can be checked against what a
// caller already observed, without keeping the file open or trusting the filesystem in between.
// It carries no lock or catalog concepts; that vocabulary lives in internal/compilerlock.
package sourcefile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxSourceFileBytes bounds how much of a source file SnapshotSourceFile and SourceBytes will
// read.
const MaxSourceFileBytes int64 = 64 << 20

// ErrSourceChanged reports that a source file's identity, contents, or the module root itself
// changed between when a snapshot was taken and when it was revalidated or read.
var ErrSourceChanged = errors.New("sourcefile: source changed")

type sourceFileIdentity struct {
	info    os.FileInfo
	modTime int64
	size    int64
	mode    os.FileMode
}

type sourceLink struct {
	identity sourceFileIdentity
	text     string
}

// SourceFileSnapshot is invocation-local evidence for one source file.
// Its fields are private so filesystem paths, identities, and handles cannot leak.
type SourceFileSnapshot struct {
	root     string
	rootID   sourceFileIdentity
	relative string
	links    []sourceLink
	target   sourceFileIdentity
	size     int64
	digest   string
	bytes    []byte
}

// Path returns the module-relative path the snapshot was taken for.
func (s SourceFileSnapshot) Path() string { return s.relative }

// Bytes returns a copy of the file's contents at snapshot time.
func (s SourceFileSnapshot) Bytes() []byte { return append([]byte(nil), s.bytes...) }

// SHA256 returns the lowercase hex SHA-256 digest of the file's contents at snapshot time.
func (s SourceFileSnapshot) SHA256() string { return s.digest }

// SnapshotSourceFile reads moduleRelativePath under moduleRoot and returns a snapshot of it,
// refusing a path that is empty, escapes moduleRoot, or names a non-regular file, and refusing a
// file that changed while it was being read.
func SnapshotSourceFile(moduleRoot, moduleRelativePath string) (SourceFileSnapshot, error) {
	root, rootID, err := snapshotRoot(moduleRoot)
	if err != nil {
		return SourceFileSnapshot{}, err
	}
	if moduleRelativePath == "." {
		return SourceFileSnapshot{}, fmt.Errorf("sourcefile: invalid relative path %q", moduleRelativePath)
	}
	relative, err := NormalizePath(moduleRelativePath)
	if err != nil {
		return SourceFileSnapshot{}, err
	}
	evidence, target, err := resolveSource(root, relative)
	if err != nil {
		return SourceFileSnapshot{}, err
	}
	b, st, err := readSource(target)
	if err != nil {
		return SourceFileSnapshot{}, err
	}
	postLinks, postTarget, postErr := resolveSource(root, relative)
	postStat, postStatErr := os.Stat(postTarget)
	if postErr != nil || postStatErr != nil || postTarget != target || !sameLinks(evidence, postLinks) || !sameIdentity(fileIdentity(target, st), fileIdentity(postTarget, postStat)) {
		return SourceFileSnapshot{}, sourceChanged(relative, "source changed during read")
	}
	currentRoot, rootErr := os.Stat(root)
	if rootErr != nil || !sameRootIdentity(rootID, fileIdentity(root, currentRoot)) {
		return SourceFileSnapshot{}, sourceChanged(relative, "module root changed during read")
	}
	h := sha256.Sum256(b)
	return SourceFileSnapshot{root: root, rootID: rootID, relative: relative, links: evidence,
		target: fileIdentity(target, st), size: int64(len(b)), digest: hex.EncodeToString(h[:]),
		bytes: append([]byte(nil), b...)}, nil
}

// Revalidate is retained for callers that hold one snapshot. It uses the root captured at snapshot time.
func (s SourceFileSnapshot) Revalidate() error {
	return revalidateOne(s.root, s.rootID, s)
}

// RevalidateSourceFiles revalidates every snapshot in snapshots against moduleRoot, in path order,
// and refuses a duplicate path.
func RevalidateSourceFiles(moduleRoot string, snapshots []SourceFileSnapshot) error {
	ordered := append([]SourceFileSnapshot(nil), snapshots...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].relative < ordered[j].relative })
	firstPath := ""
	if len(ordered) != 0 {
		firstPath = ordered[0].relative
	}
	root, rootID, err := snapshotRoot(moduleRoot)
	if err != nil {
		return sourceChanged(firstPath, "module root changed")
	}
	for i := range ordered {
		if i > 0 && ordered[i].relative == ordered[i-1].relative {
			return sourceChanged(ordered[i].relative, "duplicate path")
		}
		if err := revalidateOne(root, rootID, ordered[i]); err != nil {
			return err
		}
	}
	return nil
}

// SourceBytes reads a source file with the same input limit SnapshotSourceFile enforces.
func SourceBytes(name string) ([]byte, error) {
	st, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("sourcefile: source is not regular: %s", name)
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := io.LimitReader(f, MaxSourceFileBytes+1)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > MaxSourceFileBytes {
		return nil, fmt.Errorf("sourcefile: source exceeds %d bytes", MaxSourceFileBytes)
	}
	return b, nil
}

// NormalizePath refuses an absolute path, a path that is not already clean, a Windows-style path,
// and a path that climbs above its starting directory.
func NormalizePath(p string) (string, error) {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || len(p) >= 3 && p[:3] == "../" || len(p) >= 2 && p[1] == ':' || strings.Contains(p, `\`) {
		return "", fmt.Errorf("sourcefile: invalid relative path %q", p)
	}
	return p, nil
}

func snapshotRoot(input string) (string, sourceFileIdentity, error) {
	if input == "" || !filepath.IsAbs(input) || filepath.Clean(input) != input {
		return "", sourceFileIdentity{}, fmt.Errorf("sourcefile: module root must be an absolute clean path")
	}
	root, err := filepath.EvalSymlinks(input)
	if err != nil {
		return "", sourceFileIdentity{}, err
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("module root is not a directory")
		}
		return "", sourceFileIdentity{}, err
	}
	return root, fileIdentity(root, st), nil
}

func resolveSource(root, relative string) ([]sourceLink, string, error) {
	links := make([]sourceLink, 0, 2)
	current := root
	pending := strings.Split(filepath.ToSlash(relative), "/")
	for steps := 0; len(pending) > 0 && steps < 256; steps++ {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if current == root {
				return nil, "", fmt.Errorf("%w: %s: source escapes module root", ErrSourceChanged, relative)
			}
			current = filepath.Dir(current)
			continue
		}
		candidate := filepath.Join(current, filepath.FromSlash(part))
		if !insideRoot(root, candidate) {
			return nil, "", fmt.Errorf("%w: %s: source escapes module root", ErrSourceChanged, relative)
		}
		st, err := os.Lstat(candidate)
		if err != nil {
			return nil, "", sourceChanged(relative, err.Error())
		}
		if st.Mode()&os.ModeSymlink == 0 {
			current = candidate
			continue
		}
		text, err := os.Readlink(candidate)
		if err != nil {
			return nil, "", sourceChanged(relative, err.Error())
		}
		links = append(links, sourceLink{identity: fileIdentity(candidate, st), text: text})
		if filepath.IsAbs(text) {
			if text == root {
				current = root
				text = ""
			} else if strings.HasPrefix(text, root+string(filepath.Separator)) {
				current = root
				text = strings.TrimPrefix(text, root+string(filepath.Separator))
			} else {
				current = string(filepath.Separator)
			}
		} else {
			current = filepath.Dir(candidate)
		}
		targetParts := strings.Split(filepath.ToSlash(text), "/")
		pending = append(targetParts, pending...)
	}
	if len(pending) != 0 {
		return nil, "", sourceChanged(relative, "too many symlink levels")
	}
	if !insideRoot(root, current) {
		return nil, "", fmt.Errorf("%w: %s: source escapes module root", ErrSourceChanged, relative)
	}
	st, err := os.Stat(current)
	if err != nil {
		return nil, "", sourceChanged(relative, err.Error())
	}
	if !st.Mode().IsRegular() {
		return nil, "", fmt.Errorf("sourcefile: source is not regular: %s", relative)
	}
	return links, current, nil
}

func readSource(name string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}
	st, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return nil, nil, statErr
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("sourcefile: source is not regular: %s", name)
	}
	b, readErr := io.ReadAll(io.LimitReader(f, MaxSourceFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if int64(len(b)) > MaxSourceFileBytes {
		return nil, nil, fmt.Errorf("sourcefile: source exceeds %d bytes", MaxSourceFileBytes)
	}
	return b, st, nil
}

func fileIdentity(name string, info os.FileInfo) sourceFileIdentity {
	if info == nil {
		return sourceFileIdentity{}
	}
	return sourceFileIdentity{info: info, modTime: info.ModTime().UnixNano(), size: info.Size(), mode: info.Mode()}
}

func sameIdentity(a, b sourceFileIdentity) bool {
	return a.info != nil && b.info != nil && os.SameFile(a.info, b.info) && a.modTime == b.modTime && a.size == b.size && a.mode == b.mode
}

func sameRootIdentity(a, b sourceFileIdentity) bool {
	return a.info != nil && b.info != nil && os.SameFile(a.info, b.info)
}

func sameLinks(a, b []sourceLink) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].text != b[i].text || !sameIdentity(a[i].identity, b[i].identity) {
			return false
		}
	}
	return true
}

func insideRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(candidate))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func revalidateOne(root string, rootID sourceFileIdentity, snapshot SourceFileSnapshot) error {
	if !sameRootIdentity(rootID, snapshot.rootID) {
		return sourceChanged(snapshot.relative, "module root changed")
	}
	currentRoot, err := os.Stat(root)
	if err != nil || !sameRootIdentity(rootID, fileIdentity(root, currentRoot)) {
		return sourceChanged(snapshot.relative, "module root changed")
	}
	links, target, err := resolveSource(root, snapshot.relative)
	if err != nil {
		return wrapChanged(snapshot.relative, err)
	}
	if len(links) != len(snapshot.links) {
		return sourceChanged(snapshot.relative, "symlink chain changed")
	}
	for i := range links {
		if links[i].text != snapshot.links[i].text || !sameIdentity(links[i].identity, snapshot.links[i].identity) {
			return sourceChanged(snapshot.relative, "symlink chain changed")
		}
	}
	b, st, err := readSource(target)
	if err != nil {
		return wrapChanged(snapshot.relative, err)
	}
	if !sameIdentity(fileIdentity(target, st), snapshot.target) || int64(len(b)) != snapshot.size {
		return sourceChanged(snapshot.relative, "target changed")
	}
	postLinks, postTarget, postErr := resolveSource(root, snapshot.relative)
	postStat, postStatErr := os.Stat(postTarget)
	if postErr != nil || postStatErr != nil || postTarget != target || !sameLinks(links, postLinks) || !sameIdentity(fileIdentity(target, st), fileIdentity(postTarget, postStat)) {
		return sourceChanged(snapshot.relative, "source changed during read")
	}
	currentRoot, rootErr := os.Stat(root)
	if rootErr != nil || !sameRootIdentity(rootID, fileIdentity(root, currentRoot)) {
		return sourceChanged(snapshot.relative, "module root changed during read")
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != snapshot.digest {
		return sourceChanged(snapshot.relative, "contents changed")
	}
	return nil
}

func sourceChanged(path, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrSourceChanged, path, reason)
}

func wrapChanged(path string, err error) error {
	if errors.Is(err, ErrSourceChanged) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrSourceChanged, path, err)
}
