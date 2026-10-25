package bash_test

import (
	"context"
	"io"
	"io/fs"
	"runtime/metrics"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/adapter/bash"
)

// generated is a file system with one file, /gen, of size bytes of short lines
// that it makes as they are read: nothing of it is held.
type generated struct {
	size int64
	read atomic.Int64
}

func (g *generated) Open(name string) (fs.File, error) {
	switch name {
	case ".":
		return &endlessDir{}, nil
	case "gen":
		return &generatedFile{g: g}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type generatedFile struct {
	g   *generated
	off int64
}

func (f *generatedFile) Stat() (fs.FileInfo, error) { return endlessInfo{}, nil }
func (f *generatedFile) Close() error               { return nil }
func (f *generatedFile) Read(p []byte) (int, error) {
	if f.off >= f.g.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), f.g.size-f.off))
	for i := range n {
		// Lines of 16 bytes: "row " and a letter of 8 kinds, then padding.
		pos := (f.off + int64(i)) % 16
		switch {
		case pos == 15:
			p[i] = '\n'
		case pos < 4:
			p[i] = "row "[pos]
		case pos == 4:
			p[i] = byte('a' + (f.off+int64(i))/16%8)
		default:
			p[i] = '.'
		}
	}
	f.off += int64(n)
	f.g.read.Add(int64(n))
	return n, nil
}

// A pipeline over a large generated input holds a line at a time: reading 64
// MiB through streaming commands keeps the heap far below the input size.
func TestPipelineMemoryIsBounded(t *testing.T) {
	g := &generated{size: 64 << 20}
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	base := sample[0].Value.Uint64()
	var peak atomic.Uint64
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				metrics.Read(s)
				if v := s[0].Value.Uint64(); v > peak.Load() {
					peak.Store(v)
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := bash.Run(ctx, "cat /gen | grep row | cut -c1-5 | uniq -c | wc -l", g)
	close(stop)
	<-finished
	if err != nil || result.ExitCode != 0 || result.Stdout == "" {
		t.Fatalf("%+v %v", result, err)
	}
	if g.read.Load() != g.size {
		t.Fatalf("read %d of %d bytes", g.read.Load(), g.size)
	}
	if growth := int64(peak.Load()) - int64(base); growth > 24<<20 {
		t.Fatalf("the live heap grew by %d MiB over a %d MiB input", growth>>20, g.size>>20)
	}
}
