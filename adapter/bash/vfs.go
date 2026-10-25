package bash

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"

	"mvdan.cc/sh/v3/interp"
)

// The handlers below are the file system of the interpreter. The default
// handlers open, stat and list files of the machine; these send every
// operation to the fs.FS, and nothing to the machine.

// fsName turns a path of the script into a name in the fs.FS: the working
// directory is joined to a relative path, the result is cleaned, and the root
// "/" becomes ".".
func fsName(dir, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(dir, p)
	}
	p = path.Clean(p)
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "" {
		return "."
	}
	return p
}

const devNull = "/dev/null"

// openHandler opens a path for reading from fsys. It refuses every mode that
// writes, except on /dev/null, which discards what is written and reads empty.
func openHandler(fsys fs.FS) interp.OpenHandlerFunc {
	return func(ctx context.Context, p string, flag int, _ os.FileMode) (io.ReadWriteCloser, error) {
		if p == devNull {
			return nullFile{}, nil
		}
		if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
			return nil, &os.PathError{Op: "open", Path: p, Err: syscall.EROFS}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hc := interp.HandlerCtx(ctx)
		file, err := fsys.Open(fsName(hc.Dir, p))
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: p, Err: errnoOf(err)}
		}
		return &readOnlyFile{File: file, ctx: ctx}, nil
	}
}

// readOnlyFile is an fs.File as the interpreter's file: a write fails.
type readOnlyFile struct {
	fs.File
	ctx context.Context
}

func (f *readOnlyFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(p)
}

func (*readOnlyFile) Write([]byte) (int, error) { return 0, syscall.EROFS }

// nullFile is /dev/null.
type nullFile struct{}

func (nullFile) Read([]byte) (int, error)    { return 0, io.EOF }
func (nullFile) Write(p []byte) (int, error) { return len(p), nil }
func (nullFile) Close() error                { return nil }

func readDirHandler(fsys fs.FS) interp.ReadDirHandlerFunc2 {
	return func(ctx context.Context, p string) ([]fs.DirEntry, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fs.ReadDir(fsys, fsName(interp.HandlerCtx(ctx).Dir, p))
	}
}

func statHandler(fsys fs.FS) interp.StatHandlerFunc {
	return func(ctx context.Context, p string, _ bool) (fs.FileInfo, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fs.Stat(fsys, fsName(interp.HandlerCtx(ctx).Dir, p))
	}
}

// accessHandler denies every access check, which only a builtin or a test
// operator makes, and neither can run.
func accessHandler(context.Context, string, interp.AccessMode) error {
	return syscall.EACCES
}

// errnoOf maps the error of an fs.FS to the error whose text GNU tools print.
func errnoOf(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, fs.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, fs.ErrInvalid):
		return syscall.EINVAL
	}
	return err
}
