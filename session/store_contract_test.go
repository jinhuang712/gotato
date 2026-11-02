package session_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestCheckID(t *testing.T) {
	for _, c := range []struct {
		id string
		ok bool
	}{
		{"abc", true},
		{"Abc-1_2.3", true},
		{strings.Repeat("x", session.MaxIDBytes), true},
		{strings.Repeat("会", 23), true}, // 69 bytes: only the FileStore refuses its file name
		{"café", true},
		{"", false},
		{strings.Repeat("x", session.MaxIDBytes+1), false},
		{"a/b", false},
		{`a\b`, false},
		{"..", false},
		{"a..b", false},
		{".hidden", false},
		{"a\x00b", false},
		{"a\nb", false},
		{"a\xffb", false},
	} {
		err := session.CheckID(c.id)
		if c.ok && err != nil {
			t.Errorf("CheckID(%q) = %v, want nil", c.id, err)
		}
		if !c.ok && (!errors.Is(err, session.ErrInvalidID) || !gotato.IsCode(err, gotato.CodeInvalidArgument)) {
			t.Errorf("CheckID(%q) = %v, want ErrInvalidID (invalid_argument)", c.id, err)
		}
	}
}

func TestCheckBatch(t *testing.T) {
	checkpoint := fileEntry(1, gotato.EventKindSessionCreated)
	fact := fileEntry(2, gotato.EventKindMessageCommitted)
	logged := record.Entry{Seq: 1, Kind: gotato.EventKindTurnStarted, Layer: record.LayerLog, Data: []byte(`{}`)}
	h := record.Header{Epoch: "e"}
	for _, c := range []struct {
		name    string
		after   uint64
		entries []record.Entry
		h       record.Header
		ok      bool
	}{
		{"a new Record", 0, []record.Entry{checkpoint, fact}, h, true},
		{"a continuation", 1, []record.Entry{fact}, h, true},
		{"no epoch", 0, []record.Entry{checkpoint}, record.Header{}, false},
		{"no Entries", 1, nil, h, false},
		{"a gap", 1, []record.Entry{fileEntry(3, gotato.EventKindMessageCommitted)}, h, false},
		{"a Seq that repeats", 1, []record.Entry{fileEntry(1, gotato.EventKindMessageCommitted)}, h, false},
		{"a Record that starts with a fact", 0, []record.Entry{fileEntry(1, gotato.EventKindMessageCommitted)}, h, false},
		{"a Record that starts with a log Entry", 0, []record.Entry{logged}, h, false},
	} {
		err := session.CheckBatch(c.after, c.entries, c.h)
		if c.ok && err != nil {
			t.Errorf("%s: CheckBatch = %v, want nil", c.name, err)
		}
		if !c.ok && !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: CheckBatch = %v, want invalid_argument", c.name, err)
		}
	}
}

// Header.LogFrom is the last Checkpoint's position while a Run is in flight,
// follows a Checkpoint written in the middle of the Run, and is 0 once the Run
// ended.
func TestWriterSetsLogFromWhileARunIsInFlight(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, record.New(record.WithID("lf")), "a")
	if err != nil {
		t.Fatal(err)
	}
	load := func() record.Loaded {
		t.Helper()
		loaded, err := store.Load(ctx, "lf")
		if err != nil {
			t.Fatal(err)
		}
		return loaded
	}
	if loaded := load(); loaded.Header.LogFrom != 0 {
		t.Fatalf("LogFrom with no Run = %d, want 0", loaded.Header.LogFrom)
	}
	if _, err := writer.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("hi"))
	loaded := load()
	if loaded.Header.LogFrom != 1 || stateOf(t, loaded).Running != "run-1" {
		t.Fatalf("LogFrom = %d, Running %q, want 1 (session_created) and run-1", loaded.Header.LogFrom, stateOf(t, loaded).Running)
	}
	// The Load holds the log Entries of the Run: run_started is one.
	var kinds []gotato.EventKind
	for _, e := range loaded.Entries {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Contains(kinds, gotato.EventKindRunStarted) {
		t.Fatalf("Load of a Run in flight returned %v, want the log Entries from the Checkpoint on, run_started among them", kinds)
	}
	if _, err := writer.Record().CloseSegment(gotato.CompactionWhyOnDemand, gotato.Segment{Messages: []gotato.Message{gotato.UserMessage("hi")}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	loaded = load()
	if loaded.Header.LogFrom != loaded.Entries[0].Seq || !loaded.Entries[0].IsCheckpoint() || loaded.Entries[0].Seq == 1 {
		t.Fatalf("LogFrom = %d with Entries starting at %d (%s), want the new Checkpoint's position", loaded.Header.LogFrom, loaded.Entries[0].Seq, loaded.Entries[0].Kind)
	}
	if _, err := writer.Write(ctx, []gotato.Event{gotato.RunEnded{Header: gotato.NewHeader("", "run-1", 0), Status: gotato.RunStatusCompleted}}); err != nil {
		t.Fatal(err)
	}
	if loaded := load(); loaded.Header.LogFrom != 0 || stateOf(t, loaded).Running != "" {
		t.Fatalf("LogFrom after the Run ended = %d, Running %q, want 0 and none", loaded.Header.LogFrom, stateOf(t, loaded).Running)
	}
}

// Both reference stores return every Entry from LogFrom with the facts, and
// each Entry once, though a fact may be after LogFrom too.
func TestReferenceStoresLoadHonorsLogFrom(t *testing.T) {
	ctx := context.Background()
	files, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]session.Records{"memory": session.NewMemoryStore(), "file": files} {
		h := fileSummary(1)
		if err := store.Create(ctx, "r", []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, h); err != nil {
			t.Fatal(err)
		}
		more := []record.Entry{
			{Seq: 2, Kind: gotato.EventKindRunStarted, Layer: record.LayerLog, Data: []byte(`{}`)},
			fileEntry(3, gotato.EventKindMessageCommitted),
			{Seq: 4, Kind: gotato.EventKindReplyPartKept, Layer: record.LayerLog, Data: []byte(`{}`)},
		}
		h = fileSummary(4)
		h.LogFrom = 1
		if err := store.Append(ctx, "r", 1, more, h); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.Load(ctx, "r")
		if err != nil || loaded.Seq != 4 || fmt.Sprint(seqsOf(loaded.Entries)) != "[1 2 3 4]" || loaded.Header.LogFrom != 1 {
			t.Fatalf("%s: Load with LogFrom 1 = Seq %d, Entries %v, LogFrom %d (%v), want [1 2 3 4]", name, loaded.Seq, seqsOf(loaded.Entries), loaded.Header.LogFrom, err)
		}
		h = fileSummary(5)
		if err := store.Append(ctx, "r", 4, []record.Entry{fileEntry(5, gotato.EventKindMessageCommitted)}, h); err != nil {
			t.Fatal(err)
		}
		loaded, err = store.Load(ctx, "r")
		if err != nil || fmt.Sprint(seqsOf(loaded.Entries)) != "[1 3 5]" {
			t.Fatalf("%s: Load with no LogFrom = Entries %v (%v), want the facts [1 3 5]", name, seqsOf(loaded.Entries), err)
		}
	}
}

func seqsOf(entries []record.Entry) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}

// A FileStore pages its listing by Limit and Cursor, the most recently updated
// first, and refuses a Cursor it did not make.
func TestFileStoreListPages(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		id := fmt.Sprintf("r%d", i)
		h := record.Header{Epoch: "e", Info: record.Info{ID: id, UpdatedAt: base.Add(time.Duration(i) * time.Minute)}}
		if err := store.Create(ctx, id, []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, h); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	q := session.ListQuery{Limit: 2}
	pages := 0
	for {
		page, err := store.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Infos) > 2 {
			t.Fatalf("a page of %d with Limit 2", len(page.Infos))
		}
		for _, info := range page.Infos {
			got = append(got, info.ID)
		}
		if page.Next == "" {
			break
		}
		q.Cursor = page.Next
	}
	if want := []string{"r4", "r3", "r2", "r1", "r0"}; !slices.Equal(got, want) || pages != 3 {
		t.Fatalf("paged %v in %d pages, want %v in 3", got, pages, want)
	}
	if _, err := store.List(ctx, session.ListQuery{Cursor: "not a cursor"}); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("List with a foreign Cursor = %v, want invalid_argument", err)
	}
	// A listing with no Limit is a page of the store's own size.
	page, err := store.List(ctx, session.ListQuery{})
	if err != nil || len(page.Infos) != 5 || page.Next != "" {
		t.Fatalf("List with no Limit = %d Infos, Next %q (%v), want all 5 and no Next", len(page.Infos), page.Next, err)
	}
}

// A store times its Liveness rows by the clock it was given.
func TestStoresTimeLivenessByTheirClock(t *testing.T) {
	ctx := context.Background()
	var clock fakeClock
	files, err := session.NewFileStore(t.TempDir(), session.WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]session.Liveness{"memory": session.NewMemoryStore(session.WithClock(clock.now)), "file": files} {
		id := "instance-" + name
		if err := store.Beat(ctx, session.InstanceRow{ID: id}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if state, err := store.Instance(ctx, id); err != nil || !state.Alive {
			t.Fatalf("%s: a fresh row = %+v, %v, want alive", name, state, err)
		}
		clock.advance(2 * time.Minute)
		if state, err := store.Instance(ctx, id); err != nil || state.Alive {
			t.Fatalf("%s: a row past its ttl = %+v, %v, want not alive", name, state, err)
		}
		if err := store.Beat(ctx, session.InstanceRow{ID: id}, time.Minute); !errors.Is(err, session.ErrExpired) {
			t.Fatalf("%s: Beat of an expired row = %v, want ErrExpired", name, err)
		}
	}
}
