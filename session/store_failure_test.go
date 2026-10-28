package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Document is a state, not an identity: a Record rebuilt from one is a new
// Record, with an epoch of its own, so the writer of a deleted Record cannot
// write into its re-creation.
func TestFromDocumentGivesTheRecordANewEpoch(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	old, err := session.CreateWriter(ctx, store, session.New(session.WithID("x")), "a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	doc := old.Record().Snapshot()
	if err := store.Delete(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	r, err := session.FromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordEpoch() == "" || r.RecordEpoch() == old.Record().RecordEpoch() {
		t.Fatalf("epoch of the rebuilt Record = %q, the old one's %q: want a new one", r.RecordEpoch(), old.Record().RecordEpoch())
	}
	if _, err := session.CreateWriter(ctx, store, r, "a", session.WithIdle()); err != nil {
		t.Fatal(err)
	}
	old.Record().Set("k", "v")
	if err := old.Flush(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("stale writer flush = %v, want ErrNotFound", err)
	}
	loaded, err := store.Load(ctx, "x")
	if err != nil || loaded.Info.Metadata["k"] != "" {
		t.Fatalf("the re-created Record took the stale write: %+v, %v", loaded.Info, err)
	}
}

// failingStore fails the calls it is asked to, with a plain error.
type failingStore struct {
	*session.MemoryStore
	create, load bool
}

var errDisk = errors.New("disk on fire")

func (s failingStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.create {
		return errDisk
	}
	return s.MemoryStore.Create(ctx, id, entries, summary)
}

func (s failingStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	if s.load {
		return session.Loaded{}, errDisk
	}
	return s.MemoryStore.Load(ctx, id)
}

func TestCreateWriterAndOpenWriterClassifyStoreErrors(t *testing.T) {
	ctx := context.Background()
	mem := session.NewMemoryStore()
	_, err := session.CreateWriter(ctx, failingStore{MemoryStore: mem, create: true}, session.New(session.WithID("x")), "a")
	if !gotato.IsCode(err, gotato.CodeStoreFailure) || !errors.Is(err, errDisk) {
		t.Fatalf("CreateWriter error = %v, want store_failure that keeps its cause", err)
	}
	if _, err := session.CreateWriter(ctx, mem, session.New(session.WithID("y")), "a", session.WithIdle()); err != nil {
		t.Fatal(err)
	}
	_, err = session.OpenWriter(ctx, failingStore{MemoryStore: mem, load: true}, "y", "a")
	if !gotato.IsCode(err, gotato.CodeStoreFailure) || !errors.Is(err, errDisk) {
		t.Fatalf("OpenWriter error = %v, want store_failure", err)
	}
	// The sentinels and the caller's own context pass through.
	if _, err := session.OpenWriter(ctx, mem, "missing", "a"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("OpenWriter of a missing Record = %v, want ErrNotFound", err)
	}
	if _, err := session.CreateWriter(ctx, mem, session.New(session.WithID("y")), "a"); !errors.Is(err, session.ErrExists) {
		t.Fatalf("CreateWriter of a taken ID = %v, want ErrExists", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := session.OpenWriter(canceled, mem, "y", "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenWriter with a canceled context = %v, want context.Canceled", err)
	}
}

// movingStore ends the Run and releases the Session in its Log, before it
// answers: the Record moves between Load and Log.
type movingStore struct {
	*session.MemoryStore
	holder *session.Writer
	moved  bool
}

func (s *movingStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if !s.moved {
		s.moved = true
		if _, err := s.holder.Write(ctx, []gotato.Event{gotato.RunEnded{Header: gotato.NewHeader("", "run-1", 0), Status: gotato.RunStatusCompleted}}); err != nil {
			return nil, err
		}
		if err := s.holder.Release(ctx); err != nil {
			return nil, err
		}
	}
	return s.MemoryStore.Log(ctx, id, after, limit)
}

func TestOpenWriterOfARecordThatMovedUnderItDoesNotEndARunTwice(t *testing.T) {
	ctx := context.Background()
	mem := session.NewMemoryStore()
	holder, err := session.CreateWriter(ctx, mem, session.New(session.WithID("x")), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
		t.Fatal(err)
	}
	store := &movingStore{MemoryStore: mem, holder: holder}
	_, err = session.OpenWriter(ctx, store, "x", "a")
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
	if !errors.Is(err, session.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict (load again)", err)
	}
	// Opening again sees the settled Record.
	if _, err := session.OpenWriter(ctx, mem, "x", "a"); err != nil {
		t.Fatal(err)
	}
}

// A liveness row torn by a power cut (a renamed file with no content) is a
// dead row, not an error.
func TestFileStoreInstanceReadsATornRowAsNotAlive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "dead"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".instances", "dead.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := store.Instance(ctx, "dead")
	if err != nil || state.Alive {
		t.Fatalf("Instance of a torn row = %+v, %v; want not alive and no error", state, err)
	}
}

// Every store refuses the IDs the FileStore cannot name: 33 CJK characters are
// 99 bytes, and 297 characters of file name.
func TestEveryStoreRefusesAnIDWhoseFileNameIsTooLong(t *testing.T) {
	ctx := context.Background()
	fileStore, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("会", 33)
	for name, store := range map[string]session.Store{"memory": session.NewMemoryStore(), "file": fileStore} {
		err := store.Create(ctx, id, []session.Entry{{Seq: 1, Kind: gotato.EventKindSessionCreated, Layer: session.LayerFact, Data: []byte(`{}`)}}, session.RecordInfo{ID: id, RecordEpoch: "e"})
		if !errors.Is(err, session.ErrInvalidID) {
			t.Fatalf("%s Create(%d bytes of CJK) = %v, want ErrInvalidID", name, len(id), err)
		}
	}
}
