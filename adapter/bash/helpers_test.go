package bash_test

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jinhuang712/gotato/adapter/bash"
)

// mapFS builds a file system from name -> content; the names are paths without
// the leading slash.
func mapFS(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content), Mode: 0o444}
	}
	return m
}

// run runs a command over files and fails the test on an error of Run itself.
func run(t *testing.T, command string, files map[string]string, opts ...bash.Option) bash.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := bash.Run(ctx, command, mapFS(files), opts...)
	if err != nil {
		t.Fatalf("Run(%q) error: %v", command, err)
	}
	return result
}

// endless is a file system with one file, /inf, whose lines never end. It
// counts the bytes read from it.
type endless struct {
	read atomic.Int64
}

func (e *endless) Open(name string) (fs.File, error) {
	switch name {
	case ".":
		return &endlessDir{}, nil
	case "inf":
		return &endlessFile{e: e}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type endlessDir struct{}

func (*endlessDir) Stat() (fs.FileInfo, error) { return endlessInfo{dir: true}, nil }
func (*endlessDir) Read([]byte) (int, error)   { return 0, io.EOF }
func (*endlessDir) Close() error               { return nil }

type endlessInfo struct{ dir bool }

func (endlessInfo) Name() string { return "inf" }
func (endlessInfo) Size() int64  { return 0 }
func (i endlessInfo) Mode() fs.FileMode {
	return map[bool]fs.FileMode{true: fs.ModeDir | 0o555, false: 0o444}[i.dir]
}
func (endlessInfo) ModTime() time.Time { return time.Time{} }
func (i endlessInfo) IsDir() bool      { return i.dir }
func (endlessInfo) Sys() any           { return nil }

type endlessFile struct{ e *endless }

func (f *endlessFile) Stat() (fs.FileInfo, error) { return endlessInfo{}, nil }
func (f *endlessFile) Close() error               { return nil }
func (f *endlessFile) Read(p []byte) (int, error) {
	const line = "the same line again and again\n"
	n := copy(p, strings.Repeat(line, len(p)/len(line)+1))
	f.e.read.Add(int64(n))
	return n, nil
}
