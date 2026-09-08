package changeplan

import (
	"io"
	"os"
	"path/filepath"
)

func Read(name string) (Plan, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Plan{}, err
	}
	return Decode(b)
}

func Write(name string, p Plan) error {
	b, err := Encode(p)
	if err != nil {
		return err
	}
	dir := filepath.Dir(name)
	f, err := os.CreateTemp(dir, ".migration-plan-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	n, err := f.Write(b)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(b) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
