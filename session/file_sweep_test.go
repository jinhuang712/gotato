package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// touch makes a leftover: a directory (or a file) of the given age.
func touch(t *testing.T, path string, dir bool, age time.Duration) {
	t.Helper()
	if dir {
		if err := os.MkdirAll(filepath.Join(path, "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A store opened on a directory removes the crash leftovers a day old or
// older, and keeps a young leftover (a live writer's) and every Record.
func TestFileStoreSweepsOldLeftoversAtOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Create(ctx, "r", []session.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	old := 48 * time.Hour
	oldCreating := filepath.Join(dir, ".creating-old")
	oldDeleted := filepath.Join(dir, ".deleted-r-old")
	youngCreating := filepath.Join(dir, ".creating-young")
	youngDeleted := filepath.Join(dir, ".deleted-r-young")
	oldTmp := filepath.Join(dir, ".instances", ".tmp-old")
	youngTmp := filepath.Join(dir, ".instances", ".tmp-young")
	if err := os.MkdirAll(filepath.Join(dir, ".instances"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, oldCreating, true, old)
	touch(t, oldDeleted, true, old)
	touch(t, youngCreating, true, time.Minute)
	touch(t, youngDeleted, true, time.Minute)
	touch(t, oldTmp, false, old)
	touch(t, youngTmp, false, time.Minute)

	// A second store on the same directory, as another process would open it.
	second, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{oldCreating, oldDeleted, oldTmp} {
		if exists(p) {
			t.Errorf("%s survived the sweep", p)
		}
	}
	for _, p := range []string{youngCreating, youngDeleted, youngTmp} {
		if !exists(p) {
			t.Errorf("%s is young and must stay", p)
		}
	}
	// Both stores still work on the one directory.
	for _, s := range []*session.FileStore{first, second} {
		if _, err := s.Load(ctx, "r"); err != nil {
			t.Fatalf("Load after the sweep: %v", err)
		}
	}
	if err := second.Append(ctx, "r", 1, []session.Entry{fileEntry(2, gotato.EventKindMessageCommitted)}, fileSummary(2)); err != nil {
		t.Fatal(err)
	}
	if err := first.Delete(ctx, "r"); err != nil {
		t.Fatal(err)
	}
}

// A write's temporary file inside a Record's directory is swept when the
// directory is read, if it is old; a Record's batch files are never touched.
func TestFileStoreSweepsOldTempFilesOfARecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "r", []session.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	epochs, err := os.ReadDir(filepath.Join(dir, "r"))
	if err != nil || len(epochs) != 1 {
		t.Fatalf("epoch directories = %v, %v", epochs, err)
	}
	epoch := filepath.Join(dir, "r", epochs[0].Name())
	oldTmp := filepath.Join(epoch, ".tmp-old")
	youngTmp := filepath.Join(epoch, ".tmp-young")
	touch(t, oldTmp, false, 48*time.Hour)
	touch(t, youngTmp, false, time.Minute)

	other, err := session.NewFileStore(dir) // another process, with a cold cache
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := other.Load(ctx, "r")
	if err != nil || loaded.Head != 1 {
		t.Fatalf("Load = head %d, %v", loaded.Head, err)
	}
	if exists(oldTmp) {
		t.Error("an old temporary file survived a read of its Record")
	}
	if !exists(youngTmp) {
		t.Error("a young temporary file was removed")
	}
	if !exists(filepath.Join(epoch, "00000000000000000001.json")) {
		t.Error("a batch file was removed")
	}
}
