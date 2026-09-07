package compilerlock

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrSourceChanged = errors.New("compilerlock: source changed")

type SourceSnapshot struct {
	Path     string
	Relative string
	Resolved string
	Size     int64
	Mode     os.FileMode
	ModTime  int64
	SHA256   string
	bytes    []byte
}
type SourceFileSnapshot = SourceSnapshot

func SnapshotSource(name string) (SourceSnapshot, []byte, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	b, err := SourceBytes(name)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	st, err := os.Stat(name)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	h := sha256.Sum256(b)
	return SourceSnapshot{Path: name, Resolved: resolved, Size: st.Size(), Mode: st.Mode(), ModTime: st.ModTime().UnixNano(), SHA256: fmt.Sprintf("%x", h[:]), bytes: append([]byte(nil), b...)}, b, nil
}
func SnapshotSourceFile(root, relative string) (SourceFileSnapshot, error) {
	p, err := NormalizePath(relative)
	if err != nil {
		return SourceFileSnapshot{}, err
	}
	s, _, err := SnapshotSource(filepath.Join(root, filepath.FromSlash(p)))
	if err == nil {
		resolved, e := filepath.EvalSymlinks(s.Path)
		if e != nil {
			return SourceFileSnapshot{}, e
		}
		rel, e := filepath.Rel(root, resolved)
		if e != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
			return SourceFileSnapshot{}, fmt.Errorf("%w: source escapes module root", ErrSourceChanged)
		}
	}
	s.Relative = p
	return s, err
}
func (s SourceSnapshot) Bytes() []byte { return append([]byte(nil), s.bytes...) }
func (s SourceSnapshot) Record() SourceFile {
	p := s.Relative
	if p == "" {
		p = filepath.ToSlash(s.Path)
	}
	return SourceFile{Path: p, SHA256: s.SHA256}
}
func (s SourceSnapshot) Revalidate() error {
	resolved, err := filepath.EvalSymlinks(s.Path)
	if err != nil {
		return err
	}
	if resolved != s.Resolved {
		return fmt.Errorf("%w: %s", ErrSourceChanged, s.Path)
	}
	b, err := SourceBytes(s.Path)
	if err != nil {
		return err
	}
	st, err := os.Stat(s.Path)
	if err != nil {
		return err
	}
	if st.Size() != s.Size || st.Mode() != s.Mode || st.ModTime().UnixNano() != s.ModTime {
		return fmt.Errorf("%w: %s", ErrSourceChanged, s.Path)
	}
	h := sha256.Sum256(b)
	if fmt.Sprintf("%x", h[:]) != s.SHA256 {
		return fmt.Errorf("%w: %s", ErrSourceChanged, s.Path)
	}
	return nil
}
func RevalidateSourceFiles(root string, files []SourceFileSnapshot) error {
	for _, f := range files {
		resolved, err := filepath.EvalSymlinks(f.Path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
			return fmt.Errorf("%w: source escapes module root", ErrSourceChanged)
		}
		if err := f.Revalidate(); err != nil {
			return err
		}
	}
	return nil
}
