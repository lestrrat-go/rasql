package changeplan

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeAtomicFile struct {
	name     string
	calls    *[]fakeCall
	data     []byte
	chmodErr error
	writeN   int
	writeErr error
	syncErr  error
	closeErr error
}

type fakeCall struct {
	op   string
	args []string
	mode fs.FileMode
	data []byte
}

func (f *fakeAtomicFile) record(call fakeCall) { *f.calls = append(*f.calls, call) }
func (f *fakeAtomicFile) Name() string         { return f.name }
func (f *fakeAtomicFile) Chmod(mode fs.FileMode) error {
	f.record(fakeCall{op: "chmod", mode: mode})
	return f.chmodErr
}
func (f *fakeAtomicFile) Write(data []byte) (int, error) {
	f.record(fakeCall{op: "write", data: append([]byte(nil), data...)})
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	n := len(data)
	if f.writeN >= 0 {
		n = f.writeN
	}
	f.data = append(f.data[:0], data[:n]...)
	return n, nil
}
func (f *fakeAtomicFile) Sync() error {
	f.record(fakeCall{op: "sync"})
	return f.syncErr
}
func (f *fakeAtomicFile) Close() error {
	f.record(fakeCall{op: "close"})
	return f.closeErr
}

type fakeFileSystem struct {
	calls       []fakeCall
	readData    []byte
	readErr     error
	createErr   error
	createDir   string
	createPat   string
	file        *fakeAtomicFile
	renameErr   error
	removeErr   error
	destination map[string][]byte
}

func (f *fakeFileSystem) ReadFile(name string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{op: "read", args: []string{name}})
	return append([]byte(nil), f.readData...), f.readErr
}
func (f *fakeFileSystem) CreateTemp(dir, pattern string) (atomicFile, error) {
	f.calls = append(f.calls, fakeCall{op: "create", args: []string{dir, pattern}})
	f.createDir, f.createPat = dir, pattern
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.file == nil {
		f.file = &fakeAtomicFile{name: filepath.Join(dir, "temporary"), calls: &f.calls, writeN: -1}
	}
	return f.file, nil
}
func (f *fakeFileSystem) Rename(oldPath, newPath string) error {
	f.calls = append(f.calls, fakeCall{op: "rename", args: []string{oldPath, newPath}})
	if f.renameErr != nil {
		return f.renameErr
	}
	if f.destination == nil {
		f.destination = make(map[string][]byte)
	}
	f.destination[newPath] = append([]byte(nil), f.file.data...)
	return nil
}
func (f *fakeFileSystem) Remove(name string) error {
	f.calls = append(f.calls, fakeCall{op: "remove", args: []string{name}})
	return f.removeErr
}

func TestAtomicWriteFailureMatrix(t *testing.T) {
	createErr := errors.New("create")
	chmodErr := errors.New("chmod")
	writeErr := errors.New("write")
	syncErr := errors.New("sync")
	closeErr := errors.New("close")
	renameErr := errors.New("rename")
	removeErr := errors.New("remove")
	destination := filepath.Join("/destination", "plan.json")
	tests := []struct {
		name      string
		configure func(*fakeFileSystem)
		wantErr   error
		wantCalls []fakeCall
	}{
		{name: "create", configure: func(f *fakeFileSystem) { f.createErr = createErr }, wantErr: createErr,
			wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}}}},
		{name: "chmod", configure: func(f *fakeFileSystem) { f.file.chmodErr = chmodErr }, wantErr: chmodErr,
			wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
				{op: "chmod", mode: 0o600}, {op: "close"}, {op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "write error", configure: func(f *fakeFileSystem) { f.file.writeErr = writeErr }, wantErr: writeErr,
			wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
				{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "close"},
				{op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "short write", configure: func(f *fakeFileSystem) { f.file.writeN = 1 }, wantErr: io.ErrShortWrite,
			wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
				{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "close"},
				{op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "sync", configure: func(f *fakeFileSystem) { f.file.syncErr = syncErr }, wantErr: syncErr,
			wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
				{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "sync"}, {op: "close"},
				{op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "close", configure: func(f *fakeFileSystem) {
			f.file.closeErr = closeErr
			f.removeErr = removeErr
		}, wantErr: closeErr, wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
			{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "sync"}, {op: "close"},
			{op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "rename", configure: func(f *fakeFileSystem) { f.renameErr = renameErr }, wantErr: renameErr,
			wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
				{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "sync"}, {op: "close"},
				{op: "rename", args: []string{"/destination/temporary", destination}},
				{op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "remove error", configure: func(f *fakeFileSystem) {
			f.renameErr = renameErr
			f.removeErr = removeErr
		}, wantErr: renameErr, wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
			{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "sync"}, {op: "close"},
			{op: "rename", args: []string{"/destination/temporary", destination}},
			{op: "remove", args: []string{"/destination/temporary"}}}},
		{name: "success", wantCalls: []fakeCall{{op: "create", args: []string{"/destination", ".temporary-*"}},
			{op: "chmod", mode: 0o600}, {op: "write", data: []byte("replacement")}, {op: "sync"}, {op: "close"},
			{op: "rename", args: []string{"/destination/temporary", destination}}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fs := &fakeFileSystem{
				file:        &fakeAtomicFile{name: "/destination/temporary", writeN: -1},
				destination: map[string][]byte{destination: []byte("original")},
			}
			fs.file.calls = &fs.calls
			if test.configure != nil {
				test.configure(fs)
			}
			err := atomicWrite(fs, destination, ".temporary-*", []byte("replacement"))
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("atomicWrite() error = %v", err)
				}
			} else if err != test.wantErr {
				t.Fatalf("atomicWrite() error = %v, want %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(fs.calls, test.wantCalls) {
				t.Fatalf("calls = %v, want %v", fs.calls, test.wantCalls)
			}
			if test.name != "success" && string(fs.destination[destination]) != "original" {
				t.Fatalf("destination changed to %q", fs.destination[destination])
			}
			if test.name == "success" && string(fs.destination[destination]) != "replacement" {
				t.Fatalf("destination = %q, want replacement", fs.destination[destination])
			}
		})
	}
}

func TestReadDecodedFailureMatrix(t *testing.T) {
	readErr := errors.New("read")
	decodeErr := errors.New("decode")
	for _, test := range []struct {
		name      string
		readErr   error
		decodeErr error
		wantErr   error
		wantCalls int
	}{
		{name: "read", readErr: readErr, wantErr: readErr},
		{name: "decode", decodeErr: decodeErr, wantErr: decodeErr, wantCalls: 1},
		{name: "success", wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := &fakeFileSystem{readData: []byte("payload"), readErr: test.readErr}
			calls := 0
			got, err := readDecoded[string](fs, "input", func(data []byte) (string, error) {
				calls++
				if string(data) != "payload" {
					t.Fatalf("decoder data = %q", data)
				}
				return "decoded", test.decodeErr
			})
			if err != test.wantErr {
				t.Fatalf("readDecoded() error = %v, want %v", err, test.wantErr)
			}
			wantCalls := []fakeCall{{op: "read", args: []string{"input"}}}
			if !reflect.DeepEqual(fs.calls, wantCalls) {
				t.Fatalf("filesystem calls = %#v, want %#v", fs.calls, wantCalls)
			}
			if calls != test.wantCalls {
				t.Fatalf("decoder calls = %d, want %d", calls, test.wantCalls)
			}
			if test.wantErr == nil && got != "decoded" {
				t.Fatalf("decoded value = %q", got)
			}
		})
	}
}

func TestPlanFileIO(t *testing.T) {
	fixture, err := os.ReadFile("testdata/v1/valid/empty.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Decode(fixture)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, fixture) {
		t.Fatal("Encode(plan) bytes differ from stable fixture")
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "plan.json")
	if err := Write(name, plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("written bytes differ from Encode(plan)")
	}
	assertMode(t, name, 0o600)
	readPlan, err := Read(name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readPlan, plan) {
		t.Fatalf("Read() plan differs from fixture plan")
	}
	if readPlan.ID() != plan.ID() {
		t.Fatalf("Read() plan ID = %s, want %s", readPlan.ID(), plan.ID())
	}
	reencoded, err := Encode(readPlan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatal("re-encoded Read() plan bytes differ from Encode(plan)")
	}
	assertNoTemporaryFiles(t, dir, ".migration-plan-")

	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		t.Fatal(err)
	}
	assertMode(t, name, 0o644)
	if err := Write(name, plan); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("replacement bytes differ from fixture")
	}
	assertMode(t, name, 0o600)
	assertNoTemporaryFiles(t, dir, ".migration-plan-")

	old := []byte("preserve")
	if err := os.WriteFile(name, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(name, Plan{}); err == nil {
		t.Fatal("Write(Plan{}) succeeded")
	}
	got, err = os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Fatalf("invalid plan changed destination to %q", got)
	}
	assertNoTemporaryFiles(t, dir, ".migration-plan-")

	if _, err := Read(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("Read(missing) succeeded")
	}
}

func assertMode(t *testing.T, name string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode = %o, want %o", got, want)
	}
}

func assertNoTemporaryFiles(t *testing.T, dir, prefix string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("temporary file %q remains", entry.Name())
		}
	}
}
