package changeplan

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type atomicFile interface {
	Name() string
	Chmod(fs.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type fileSystem interface {
	ReadFile(string) ([]byte, error)
	CreateTemp(dir, pattern string) (atomicFile, error)
	Rename(oldPath, newPath string) error
	Remove(string) error
}

type osFileSystem struct{}

type osAtomicFile struct{ file *os.File }

func (osFileSystem) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (osFileSystem) CreateTemp(dir, pattern string) (atomicFile, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return osAtomicFile{file: file}, nil
}
func (osFileSystem) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
func (osFileSystem) Remove(name string) error             { return os.Remove(name) }

func (f osAtomicFile) Name() string                   { return f.file.Name() }
func (f osAtomicFile) Chmod(mode fs.FileMode) error   { return f.file.Chmod(mode) }
func (f osAtomicFile) Write(data []byte) (int, error) { return f.file.Write(data) }
func (f osAtomicFile) Sync() error                    { return f.file.Sync() }
func (f osAtomicFile) Close() error                   { return f.file.Close() }

func readDecoded[T any](filesystem fileSystem, name string, decode func([]byte) (T, error)) (T, error) {
	data, err := filesystem.ReadFile(name)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode(data)
}

func atomicWrite(filesystem fileSystem, name, tempPattern string, data []byte) error {
	file, err := filesystem.CreateTemp(filepath.Dir(name), tempPattern)
	if err != nil {
		return err
	}
	temporary := file.Name()
	open := true
	removeTemporary := func() {
		if open {
			_ = file.Close()
			open = false
		}
		_ = filesystem.Remove(temporary)
	}
	if err := file.Chmod(0o600); err != nil {
		removeTemporary()
		return err
	}
	written, err := file.Write(data)
	if err != nil {
		removeTemporary()
		return err
	}
	if written != len(data) {
		removeTemporary()
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		removeTemporary()
		return err
	}
	if err := file.Close(); err != nil {
		open = false
		_ = filesystem.Remove(temporary)
		return err
	}
	open = false
	if err := filesystem.Rename(temporary, name); err != nil {
		_ = filesystem.Remove(temporary)
		return err
	}
	return nil
}

func Read(name string) (Plan, error) {
	return readDecoded(osFileSystem{}, name, Decode)
}

func Write(name string, plan Plan) error {
	data, err := Encode(plan)
	if err != nil {
		return err
	}
	return atomicWrite(osFileSystem{}, name, ".migration-plan-*", data)
}

func ReadCheckpoint(name string) (Checkpoint, error) {
	return readDecoded(osFileSystem{}, name, DecodeCheckpoint)
}

func WriteCheckpoint(name string, checkpoint Checkpoint) error {
	data, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	return atomicWrite(osFileSystem{}, name, ".migration-checkpoint-*", data)
}
