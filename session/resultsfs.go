package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// ResultsDir is the directory of ResultsFS that holds the Tool Results, one
// file for each Tool call, named by its call ID.
const ResultsDir = "results"

// resultsModTime is the modification time of every file ResultsFS shows. A
// Record gives the same listing on every Instance, so no clock is read.
var resultsModTime = time.Unix(0, 0).UTC()

// ResultsFS shows the Tool Results of a Record as a read-only io/fs.FS, for a
// tool that queries the Session's own data. The root holds one directory,
// results, and results/<call_id> is the text of the Tool Result of that call:
// the full body of a spilled result, and the text Parts of any other result,
// joined with a line break as the spill joins them (gotato.SpillBody).
//
// It lists the Tool Results the Record's Context holds, in the order of their
// call IDs, so the same Record lists the same names with the same sizes on
// every Instance. A result a Segment closed over is no longer listed. The
// modification time of every entry is the Unix epoch, and files are mode 0444.
// A call ID that is not a plain file name (it holds a "/") is left out.
//
// A body is read only when its file is opened, from the live Record's cache
// when it holds it (Record.SpilledBody) and otherwise from log, the store of
// the Record, so loading the FS reads no body. ctx governs those reads and
// should last as long as the FS is used. The FS reaches only record: a call of
// another Session is not there. It uses the standard library alone, so the
// tool that wraps it needs nothing but io/fs.
func ResultsFS(ctx context.Context, record *Record, log EntryLog) fs.FS {
	return &resultsFS{ctx: ctx, record: record, log: log}
}

type resultsFS struct {
	ctx    context.Context
	record *Record
	log    EntryLog
}

// resultEntry is one file of the FS: the Tool Result of a call, without its
// body.
type resultEntry struct {
	name    string
	size    int64
	spilled *gotato.SpilledResult
	text    string // the body, when the result is not spilled
}

// results lists the Tool Results the Record holds, by call ID.
func (f *resultsFS) results() []resultEntry {
	seen := map[string]bool{}
	var out []resultEntry
	for _, message := range f.record.Messages() {
		result := message.ToolResult
		if result == nil {
			continue
		}
		name := string(result.CallID)
		if !fs.ValidPath(name) || strings.Contains(name, "/") || name == "." || seen[name] {
			continue
		}
		seen[name] = true
		entry := resultEntry{name: name}
		if result.Spilled != nil {
			spilled := *result.Spilled
			entry.spilled, entry.size = &spilled, int64(spilled.Bytes)
		} else {
			entry.text = gotato.SpillBody(result.Content)
			entry.size = int64(len(entry.text))
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Open implements fs.FS.
func (f *resultsFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	switch name {
	case ".":
		return &resultsDir{name: ".", entries: []fs.DirEntry{resultInfo{name: ResultsDir, dir: true}}}, nil
	case ResultsDir:
		entries := f.results()
		list := make([]fs.DirEntry, len(entries))
		for i, e := range entries {
			list[i] = resultInfo{name: e.name, size: e.size}
		}
		return &resultsDir{name: ResultsDir, entries: list}, nil
	}
	call, ok := strings.CutPrefix(name, ResultsDir+"/")
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	for _, e := range f.results() {
		if e.name != call {
			continue
		}
		body := e.text
		if e.spilled != nil {
			text, pruned, err := f.record.SpilledBody(f.ctx, f.log, *e.spilled)
			if errors.Is(err, ErrNotFound) {
				pruned, err = true, nil
			}
			if err != nil {
				return nil, &fs.PathError{Op: "open", Path: name, Err: err}
			}
			if pruned {
				return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("the body was removed from the Record: %w", fs.ErrNotExist)}
			}
			body = text
		}
		return &resultFile{Reader: *strings.NewReader(body), info: resultInfo{name: e.name, size: int64(len(body))}}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadDir implements fs.ReadDirFS, so a listing reads no body.
func (f *resultsFS) ReadDir(name string) ([]fs.DirEntry, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	dir, ok := file.(*resultsDir)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	return dir.ReadDir(-1)
}

// Stat implements fs.StatFS, so a size is known without reading the body.
func (f *resultsFS) Stat(name string) (fs.FileInfo, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	switch name {
	case ".":
		return resultInfo{name: ".", dir: true}, nil
	case ResultsDir:
		return resultInfo{name: ResultsDir, dir: true}, nil
	}
	if call, ok := strings.CutPrefix(name, ResultsDir+"/"); ok {
		for _, e := range f.results() {
			if e.name == call {
				return resultInfo{name: e.name, size: e.size}, nil
			}
		}
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// resultInfo is the fs.FileInfo and fs.DirEntry of a file or a directory.
type resultInfo struct {
	name string
	size int64
	dir  bool
}

func (i resultInfo) Name() string { return i.name }
func (i resultInfo) Size() int64  { return i.size }
func (i resultInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i resultInfo) ModTime() time.Time         { return resultsModTime }
func (i resultInfo) IsDir() bool                { return i.dir }
func (i resultInfo) Sys() any                   { return nil }
func (i resultInfo) Type() fs.FileMode          { return i.Mode().Type() }
func (i resultInfo) Info() (fs.FileInfo, error) { return i, nil }

// resultFile is an open Tool Result.
type resultFile struct {
	strings.Reader
	info resultInfo
}

func (f *resultFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *resultFile) Close() error               { return nil }

// resultsDir is an open directory.
type resultsDir struct {
	name    string
	entries []fs.DirEntry
	offset  int
}

func (d *resultsDir) Stat() (fs.FileInfo, error) { return resultInfo{name: d.name, dir: true}, nil }
func (d *resultsDir) Close() error               { return nil }
func (d *resultsDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}

func (d *resultsDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.offset:]
	if n <= 0 {
		d.offset = len(d.entries)
		return append([]fs.DirEntry(nil), rest...), nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(rest))
	d.offset += n
	return append([]fs.DirEntry(nil), rest[:n]...), nil
}
