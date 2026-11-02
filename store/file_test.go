package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/store/storetest"
	"github.com/jinhuang712/gotato/testkit"
)

// List reads a Record's summary from its last write alone. A damaged write in
// the middle of a Record (a stand-in for a large one) proves it.
func TestFileStoreListReadsOnlyTheLastWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, "r", []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	kinds := []gotato.EventKind{gotato.EventKindMessageCommitted, gotato.EventKindCompactionEnded, gotato.EventKindMessageCommitted}
	for i, kind := range kinds {
		seq := uint64(i + 2)
		if err := st.Append(ctx, "r", seq-1, []record.Entry{fileEntry(seq, kind)}, fileSummary(seq)); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := st.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Seq != 4 || loaded.Header.Info.Messages != 4 || len(loaded.Entries) != 2 || loaded.Entries[0].Seq != 3 {
		t.Fatalf("Load = seq %d, %d facts, want seq 4 and facts 3, 4", loaded.Seq, len(loaded.Entries))
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "r", "*", "00000000000000000002.json"))
	if len(matches) != 1 {
		t.Fatalf("found %d files for position 2", len(matches))
	}
	if err := os.WriteFile(matches[0], []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := st.List(ctx, store.ListQuery{})
	if infos := page.Infos; err != nil || len(infos) != 1 || infos[0].Messages != 4 {
		t.Fatalf("List = %+v (%v), want one Record at seq 4", infos, err)
	}
}

// A Delete whose files cannot all be removed has still deleted the Record: the
// store forgets its head, and the error says so.
func TestFileStoreDeleteForgetsARecordWhoseFilesStay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, "r", []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	epochs, _ := filepath.Glob(filepath.Join(dir, "r", "*"))
	if len(epochs) != 1 {
		t.Fatalf("found %d epoch directories", len(epochs))
	}
	// A directory nobody may write to keeps its files from being removed.
	if err := os.Chmod(epochs[0], 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		left, _ := filepath.Glob(filepath.Join(dir, ".deleted-*", "*"))
		for _, path := range left {
			_ = os.Chmod(path, 0o700)
		}
	}()
	if os.Geteuid() == 0 {
		t.Skip("root removes files from any directory")
	}
	err = st.Delete(ctx, "r")
	if err == nil {
		t.Fatal("Delete removed files from a read-only directory")
	}
	if _, err := st.Load(ctx, "r"); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("Load after Delete = %v, want ErrNotFound", err)
	}
	if n := store.CachedHeads(st); n != 0 {
		t.Fatalf("the store still keeps %d head(s) of a deleted Record", n)
	}
}

// A heartbeat held on a slow disk does not stall the writes of a Record.
func TestFileStoreBeatDoesNotBlockAppend(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, "r", []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	inBeat, release := make(chan struct{}), make(chan struct{})
	store.SetDuringBeat(st, func() {
		close(inBeat)
		<-release
	})
	beaten := make(chan error, 1)
	go func() { beaten <- st.Beat(ctx, store.InstanceRow{ID: "i"}, time.Hour) }()
	<-inBeat
	appended := make(chan error, 1)
	go func() {
		appended <- st.Append(ctx, "r", 1, []record.Entry{fileEntry(2, gotato.EventKindMessageCommitted)}, fileSummary(2))
	}()
	select {
	case err := <-appended:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("an Append waited for a heartbeat")
		appended = nil
	}
	close(release)
	if err := <-beaten; err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreRoundTripContinuesHistory(t *testing.T) {
	st, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := record.New(record.WithMetadata(map[string]string{"app": "test"}))
	writer, err := store.CreateWriter(ctx, st, s, "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s, testkit.EchoModel{}, "first")
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenWriter(ctx, st, s.ID(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	loaded := reopened.Record()
	if loaded.Len() != 2 || loaded.ParentID() != "" {
		t.Fatalf("loaded = %d messages", loaded.Len())
	}
	if value, _ := loaded.Get("app"); value != "test" {
		t.Fatalf("metadata lost: %v", loaded.Metadata())
	}
	model := testkit.NewFakeModel(testkit.Text("second answer"))
	runSession(t, loaded, model, "second")
	request, _ := model.LastRequest()
	if len(request.Messages) != 3 || gotato.TextOf(request.Messages[0]) != "first" {
		t.Fatalf("continued run saw %d messages", len(request.Messages))
	}
	if err := reopened.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := st.List(ctx, store.ListQuery{})
	if list := page.Infos; err != nil || len(list) != 1 || list[0].Messages != 4 || list[0].Runs != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := st.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, st, s.ID()); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	if _, err := store.Open(ctx, st, "../escape"); !errors.Is(err, store.ErrInvalidID) {
		t.Fatalf("path traversal err = %v", err)
	}
}

func TestFileStoreListSurfacesUnreadableSession(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "stray.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.List(context.Background(), store.ListQuery{}); err == nil {
		t.Fatal("List hid an unreadable session")
	}
}

// A file the store did not write, inside a Record's directory, is an error that
// names the path; it is neither a Record nor a missing one.
func TestFileStoreStrayFileInRecordDirIsAnError(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "r", "1.json")
	if err := os.WriteFile(stray, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = st.Load(context.Background(), "r")
	if err == nil || errors.Is(err, record.ErrNotFound) || !strings.Contains(err.Error(), stray) {
		t.Fatalf("want an error naming %s, got %v", stray, err)
	}
}

// List decodes the summary of the last write and skips its Entries: a last
// write whose Entries are not Entries still lists.
func TestFileStoreListDoesNotDecodeTheEntriesOfTheLastWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, "r", []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "r", "*", "00000000000000000001.json"))
	if len(matches) != 1 {
		t.Fatalf("found %d files for position 1", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), `"entries":[`, `"entries":[7,`, 1)
	if broken == string(data) {
		t.Fatalf("no entries array in %s", data)
	}
	if err := os.WriteFile(matches[0], []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := st.List(ctx, store.ListQuery{})
	if infos := page.Infos; err != nil || len(infos) != 1 || infos[0].Messages != 1 {
		t.Fatalf("List = %+v, %v; want the one summary", infos, err)
	}
}

func TestMemoryStoreConformance(t *testing.T) {
	records := func() (storetest.Open, func(), error) {
		st := store.NewMemoryStore()
		return func() store.Records { return st }, nil, nil
	}
	t.Run("Records", func(t *testing.T) { storetest.RunRecords(t, records) })
	t.Run("Lister", func(t *testing.T) { storetest.RunLister(t, records) })
	t.Run("Liveness", func(t *testing.T) {
		storetest.RunLiveness(t, func() (storetest.OpenLiveness, func(), error) {
			st := store.NewMemoryStore()
			return func() store.Liveness { return st }, nil, nil
		})
	})
}

// A MemoryStore times its rows by the clock it was given, so the suite moves
// that clock and sleeps none.
func TestMemoryStoreConformanceWithAClock(t *testing.T) {
	var clock fakeClock
	storetest.RunLiveness(t, func() (storetest.OpenLiveness, func(), error) {
		st := store.NewMemoryStore(store.WithClock(clock.now))
		return func() store.Liveness { return st }, nil, nil
	}, storetest.WithAdvance(clock.advance))
}

// Every handle is a FileStore of its own on one directory, as two processes
// sharing it are.
func fileBacking() (storetest.Open, func(), error) {
	dir, err := os.MkdirTemp("", "gotato-filestore-*")
	if err != nil {
		return nil, nil, err
	}
	open := func() store.Records {
		st, err := store.NewFileStore(dir)
		if err != nil {
			panic(err)
		}
		return st
	}
	return open, func() { _ = os.RemoveAll(dir) }, nil
}

func TestFileStoreConformance(t *testing.T) {
	t.Run("Records", func(t *testing.T) { storetest.RunRecords(t, fileBacking) })
	t.Run("Lister", func(t *testing.T) { storetest.RunLister(t, fileBacking) })
	t.Run("Liveness", func(t *testing.T) {
		var clock fakeClock
		storetest.RunLiveness(t, func() (storetest.OpenLiveness, func(), error) {
			dir, err := os.MkdirTemp("", "gotato-filestore-*")
			if err != nil {
				return nil, nil, err
			}
			open := func() store.Liveness {
				st, err := store.NewFileStore(dir, store.WithClock(clock.now))
				if err != nil {
					panic(err)
				}
				return st
			}
			return open, func() { _ = os.RemoveAll(dir) }, nil
		}, storetest.WithAdvance(clock.advance))
	})
}
