package compilerlock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrSourceChanged = errors.New("compilerlock: source changed")

type sourceFileIdentity struct{ info os.FileInfo }

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

func (s SourceFileSnapshot) Path() string { return s.relative }

func (s SourceFileSnapshot) Bytes() []byte { return append([]byte(nil), s.bytes...) }

func (s SourceFileSnapshot) Record() SourceFile {
	return SourceFile{Path: s.relative, SHA256: s.digest}
}

func SnapshotSourceFile(moduleRoot, moduleRelativePath string) (SourceFileSnapshot, error) {
	root, rootID, err := snapshotRoot(moduleRoot)
	if err != nil {
		return SourceFileSnapshot{}, err
	}
	if moduleRelativePath == "." {
		return SourceFileSnapshot{}, fmt.Errorf("compilerlock: invalid relative path %q", moduleRelativePath)
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
	h := sha256.Sum256(b)
	return SourceFileSnapshot{root: root, rootID: rootID, relative: relative, links: evidence,
		target: sourceFileIdentity{info: st}, size: int64(len(b)), digest: hex.EncodeToString(h[:]),
		bytes: append([]byte(nil), b...)}, nil
}

// Revalidate is retained for callers that hold one snapshot. It uses the root captured at snapshot time.
func (s SourceFileSnapshot) Revalidate() error {
	return revalidateOne(s.root, s.rootID, s)
}

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

func snapshotRoot(input string) (string, sourceFileIdentity, error) {
	if input == "" || !filepath.IsAbs(input) || filepath.Clean(input) != input {
		return "", sourceFileIdentity{}, fmt.Errorf("compilerlock: module root must be an absolute clean path")
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
	return root, sourceFileIdentity{info: st}, nil
}

func resolveSource(root, relative string) ([]sourceLink, string, error) {
	current := filepath.Join(root, filepath.FromSlash(relative))
	links := make([]sourceLink, 0, 2)
	for steps := 0; steps < 128; steps++ {
		rel, err := filepath.Rel(root, current)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, "", fmt.Errorf("%w: %s: source escapes module root", ErrSourceChanged, relative)
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		prefix := root
		restarted := false
		for i, part := range parts {
			prefix = filepath.Join(prefix, filepath.FromSlash(part))
			st, err := os.Lstat(prefix)
			if err != nil {
				return nil, "", sourceChanged(relative, err.Error())
			}
			if st.Mode()&os.ModeSymlink == 0 {
				continue
			}
			text, err := os.Readlink(prefix)
			if err != nil {
				return nil, "", sourceChanged(relative, err.Error())
			}
			links = append(links, sourceLink{identity: sourceFileIdentity{info: st}, text: text})
			target := text
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(prefix), target)
			}
			target = filepath.Clean(target)
			targetRel, relErr := filepath.Rel(root, target)
			if relErr != nil || targetRel == ".." || strings.HasPrefix(targetRel, ".."+string(filepath.Separator)) {
				return nil, "", fmt.Errorf("%w: %s: source escapes module root", ErrSourceChanged, relative)
			}
			remaining := parts[i+1:]
			current = target
			if len(remaining) != 0 {
				current = filepath.Join(append([]string{current}, remaining...)...)
			}
			restarted = true
			break
		}
		if restarted {
			continue
		}
		st, err := os.Stat(current)
		if err != nil {
			return nil, "", sourceChanged(relative, err.Error())
		}
		if !st.Mode().IsRegular() {
			return nil, "", fmt.Errorf("compilerlock: source is not regular: %s", relative)
		}
		return links, current, nil
	}
	return nil, "", sourceChanged(relative, "too many symlink levels")
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
		return nil, nil, fmt.Errorf("compilerlock: source is not regular: %s", name)
	}
	b, readErr := io.ReadAll(io.LimitReader(f, MaxSourceFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if len(b) > MaxSourceFileBytes {
		return nil, nil, fmt.Errorf("compilerlock: source exceeds %d bytes", MaxSourceFileBytes)
	}
	return b, st, nil
}

func revalidateOne(root string, rootID sourceFileIdentity, snapshot SourceFileSnapshot) error {
	if snapshot.rootID.info == nil || !os.SameFile(rootID.info, snapshot.rootID.info) {
		return sourceChanged(snapshot.relative, "module root changed")
	}
	currentRoot, err := os.Stat(root)
	if err != nil || !os.SameFile(rootID.info, currentRoot) {
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
		if links[i].text != snapshot.links[i].text || !os.SameFile(links[i].identity.info, snapshot.links[i].identity.info) {
			return sourceChanged(snapshot.relative, "symlink chain changed")
		}
	}
	b, st, err := readSource(target)
	if err != nil {
		return wrapChanged(snapshot.relative, err)
	}
	if !os.SameFile(st, snapshot.target.info) || int64(len(b)) != snapshot.size {
		return sourceChanged(snapshot.relative, "target changed")
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
