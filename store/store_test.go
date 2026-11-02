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
)

// A Document is a state, not an identity: a Record rebuilt from one is a new
// Record, with an epoch of its own, so the writer of a deleted Record cannot
// write into its re-creation.
func TestFromDocumentGivesTheRecordANewEpoch(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	old, err := store.CreateWriter(ctx, st, record.New(record.WithID("x")), "a", store.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	doc := old.Record().Snapshot()
	if err := st.Delete(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	r, err := record.FromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordEpoch() == "" || r.RecordEpoch() == old.Record().RecordEpoch() {
		t.Fatalf("epoch of the rebuilt Record = %q, the old one's %q: want a new one", r.RecordEpoch(), old.Record().RecordEpoch())
	}
	if _, err := store.CreateWriter(ctx, st, r, "a", store.WithIdle()); err != nil {
		t.Fatal(err)
	}
	old.Record().Set("k", "v")
	if err := old.Flush(ctx); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("stale writer flush = %v, want ErrNotFound", err)
	}
	loaded, err := st.Load(ctx, "x")
	if err != nil || loaded.Header.Info.Metadata["k"] != "" {
		t.Fatalf("the re-created Record took the stale write: %+v, %v", loaded.Header.Info, err)
	}
}

// failingStore fails the calls it is asked to, with a plain error.
type failingStore struct {
	*store.MemoryStore
	create, load bool
}

var errDisk = errors.New("disk on fire")

func (s failingStore) Create(ctx context.Context, id string, entries []record.Entry, h record.Header) error {
	if s.create {
		return errDisk
	}
	return s.MemoryStore.Create(ctx, id, entries, h)
}

func (s failingStore) Load(ctx context.Context, id string) (record.Loaded, error) {
	if s.load {
		return record.Loaded{}, errDisk
	}
	return s.MemoryStore.Load(ctx, id)
}

func TestCreateWriterAndOpenWriterClassifyStoreErrors(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore()
	_, err := store.CreateWriter(ctx, failingStore{MemoryStore: mem, create: true}, record.New(record.WithID("x")), "a")
	if !gotato.IsCode(err, gotato.CodeStoreFailure) || !errors.Is(err, errDisk) {
		t.Fatalf("CreateWriter error = %v, want store_failure that keeps its cause", err)
	}
	if _, err := store.CreateWriter(ctx, mem, record.New(record.WithID("y")), "a", store.WithIdle()); err != nil {
		t.Fatal(err)
	}
	_, err = store.OpenWriter(ctx, failingStore{MemoryStore: mem, load: true}, "y", "a")
	if !gotato.IsCode(err, gotato.CodeStoreFailure) || !errors.Is(err, errDisk) {
		t.Fatalf("OpenWriter error = %v, want store_failure", err)
	}
	// The sentinels and the caller's own context pass through.
	if _, err := store.OpenWriter(ctx, mem, "missing", "a"); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("OpenWriter of a missing Record = %v, want ErrNotFound", err)
	}
	if _, err := store.CreateWriter(ctx, mem, record.New(record.WithID("y")), "a"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("CreateWriter of a taken ID = %v, want ErrExists", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.OpenWriter(canceled, mem, "y", "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenWriter with a canceled context = %v, want context.Canceled", err)
	}
}

// movingStore ends the Run and releases the Session after its Load has read
// the Record, before it answers: the Record moves under the Load.
type movingStore struct {
	*store.MemoryStore
	holder *store.Writer
	moved  bool
}

func (s *movingStore) Load(ctx context.Context, id string) (record.Loaded, error) {
	loaded, err := s.MemoryStore.Load(ctx, id)
	if err != nil || s.moved {
		return loaded, err
	}
	s.moved = true
	if _, err := s.holder.Write(ctx, []gotato.Event{gotato.RunEnded{Header: gotato.NewHeader("", "run-1", 0), Status: gotato.RunStatusCompleted}}); err != nil {
		return record.Loaded{}, err
	}
	if err := s.holder.Release(ctx); err != nil {
		return record.Loaded{}, err
	}
	return loaded, nil
}

func TestOpenWriterOfARecordThatMovedUnderItDoesNotEndARunTwice(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore()
	holder, err := store.CreateWriter(ctx, mem, record.New(record.WithID("x")), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
		t.Fatal(err)
	}
	st := &movingStore{MemoryStore: mem, holder: holder}
	_, err = store.OpenWriter(ctx, st, "x", "a")
	entries, logErr := mem.Log(ctx, "x", 0, 0)
	if logErr != nil {
		t.Fatal(logErr)
	}
	ended := 0
	for _, e := range entries {
		if e.Kind == gotato.EventKindRunEnded {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("the log has %d run_ended Entries, want 1", ended)
	}
	// The claim is written at the position the Load read, which is taken now.
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict (load again)", err)
	}
	// Opening again sees the settled Record.
	if _, err := store.OpenWriter(ctx, mem, "x", "a"); err != nil {
		t.Fatal(err)
	}
}

// A liveness row torn by a power cut (a renamed file with no content) is a
// dead row, not an error.
func TestFileStoreInstanceReadsATornRowAsNotAlive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Beat(ctx, store.InstanceRow{ID: "dead"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".instances", "dead.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := st.Instance(ctx, "dead")
	if err != nil || state.Alive {
		t.Fatalf("Instance of a torn row = %+v, %v; want not alive and no error", state, err)
	}
}

// The file name rule is the FileStore's own: 23 CJK characters are 69 bytes of
// ID, so CheckID and the MemoryStore keep them, and 207 bytes of file name, so
// the FileStore refuses them.
func TestOnlyTheFileStoreRefusesAnIDWhoseFileNameIsTooLong(t *testing.T) {
	ctx := context.Background()
	fileStore, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("会", 23)
	if err := store.CheckID(id); err != nil {
		t.Fatalf("CheckID(%d bytes of CJK) = %v, want nil", len(id), err)
	}
	create := func(st store.Records) error {
		return st.Create(ctx, id, []record.Entry{{Seq: 1, Kind: gotato.EventKindSessionCreated, Layer: record.LayerFact, Data: []byte(`{}`)}}, record.Header{Epoch: "e", Info: record.Info{ID: id}})
	}
	if err := create(store.NewMemoryStore()); err != nil {
		t.Fatalf("MemoryStore Create(%d bytes of CJK) = %v, want nil", len(id), err)
	}
	if err := create(fileStore); !errors.Is(err, store.ErrInvalidID) {
		t.Fatalf("FileStore Create(%d bytes of CJK) = %v, want ErrInvalidID", len(id), err)
	}
	if _, err := fileStore.Load(ctx, id); !errors.Is(err, store.ErrInvalidID) {
		t.Fatalf("FileStore Load = %v, want ErrInvalidID", err)
	}
}

// A limit bounds how many Entries StoredEvents reads, and a page never reads
// as the end of the log while Events remain after it.
func TestStoredEventsHonorsItsLimit(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	created, err := store.CreateWriter(ctx, st, record.New(), "instance-a", store.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	id := created.Record().ID()
	all, err := store.StoredEvents(ctx, st, id, 0, 0)
	if err != nil || len(all) < 2 {
		t.Fatalf("all = %d Events, %v; want at least two", len(all), err)
	}
	var seen int
	var after uint64
	for {
		page, err := store.StoredEvents(ctx, st, id, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 1 {
			t.Fatalf("a page of %d Events with limit 1", len(page))
		}
		seen++
		after = page[len(page)-1].Head().Number
	}
	if seen != len(all) {
		t.Fatalf("paged %d Events, want %d", seen, len(all))
	}
}
