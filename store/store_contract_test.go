package store_test

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
	"github.com/jinhuang712/gotato/store"
)

func TestCheckID(t *testing.T) {
	for _, c := range []struct {
		id string
		ok bool
	}{
		{"abc", true},
		{"Abc-1_2.3", true},
		{strings.Repeat("x", store.MaxIDBytes), true},
		{strings.Repeat("会", 23), true}, // 69 bytes: only the FileStore refuses its file name
		{"café", true},
		{"", false},
		{strings.Repeat("x", store.MaxIDBytes+1), false},
		{"a/b", false},
		{`a\b`, false},
		{"..", false},
		{"a..b", false},
		{".hidden", false},
		{"a\x00b", false},
		{"a\nb", false},
		{"a\xffb", false},
	} {
		err := store.CheckID(c.id)
		if c.ok && err != nil {
			t.Errorf("CheckID(%q) = %v, want nil", c.id, err)
		}
		if !c.ok && (!errors.Is(err, store.ErrInvalidID) || !gotato.IsCode(err, gotato.CodeInvalidArgument)) {
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
		err := store.CheckBatch(c.after, c.entries, c.h)
		if c.ok && err != nil {
			t.Errorf("%s: CheckBatch = %v, want nil", c.name, err)
		}
		if !c.ok && !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: CheckBatch = %v, want invalid_argument", c.name, err)
		}
	}
}

// A Checkpoint written in the middle of a Run keeps what the Entries before it
// said: the Run in flight, the Runs and usage so far, when the last Run ended
// and the notices pending. A writer that takes the Record over from a load
// that starts at that Checkpoint ends the Run as interrupted, counts it once,
// and keeps the notice and the totals.
func TestACheckpointInTheMiddleOfARunKeepsWhatCameBefore(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("cp")), "a")
	if err != nil {
		t.Fatal(err)
	}
	usage := gotato.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}
	ended := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	runEnded := gotato.RunEnded{Header: gotato.NewHeader("", "run-0", 0), Status: gotato.RunStatusCompleted, Usage: usage}
	runEnded.Time = ended
	notice := gotato.NoticeQueued{Header: gotato.NewHeader("", "run-0", 0), MessageID: "n1", NoticeKind: "deploy", Source: gotato.NoticeSourceService, Text: "a deploy is under way"}
	if _, err := writer.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-0", 0)}, notice, runEnded, gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("hi"))
	if _, err := writer.Record().CloseSegment(gotato.CompactionWhyOnDemand, gotato.Segment{Messages: []gotato.Message{gotato.UserMessage("hi")}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(ctx, "cp")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range loaded.Entries {
		if entry.Kind == gotato.EventKindRunStarted || entry.Kind == gotato.EventKindNoticeQueued {
			t.Fatalf("the load holds %s at %d, want it to start at the Checkpoint after it", entry.Kind, entry.Seq)
		}
	}
	if !heldOf(t, loaded) {
		t.Fatal("a load from the Checkpoint does not see the Run in flight")
	}
	other, err := store.OpenWriter(ctx, st, "cp", "b")
	if err != nil {
		t.Fatal(err)
	}
	rec := other.Record()
	if runs := rec.Runs(); len(runs) != 1 || runs[0].RunID != "run-1" || runs[0].Status != gotato.RunStatusFailed || runs[0].Error != "interrupted" {
		t.Fatalf("the Runs after the takeover = %+v, want run-1 ended as interrupted", runs)
	}
	if info := rec.Info(); info.Runs != 2 || info.Usage != usage {
		t.Fatalf("Info after the takeover = Runs %d, Usage %+v; want 2 Runs and %+v", info.Runs, info.Usage, usage)
	}
	if notices := rec.PendingNotices(); len(notices) != 1 || notices[0].MessageID != "n1" {
		t.Fatalf("PendingNotices after the takeover = %+v, want n1", notices)
	}
	if !rec.LastRunEnd().After(ended) {
		t.Fatalf("LastRunEnd after the takeover = %v, want the interrupted Run's end, after %v", rec.LastRunEnd(), ended)
	}
	if err := other.Release(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := store.Open(ctx, st, "cp")
	if err != nil {
		t.Fatal(err)
	}
	if _, held := again.Holder(); held || again.Info().Runs != 2 || again.Info().Usage != usage || len(again.PendingNotices()) != 1 {
		t.Fatalf("a load after the release: held %v, Info %+v, notices %+v; want idle, 2 Runs, the usage and n1", held, again.Info(), again.PendingNotices())
	}
}

// A FileStore pages its listing by Limit and Cursor, the most recently updated
// first, and refuses a Cursor it did not make.
func TestFileStoreListPages(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		id := fmt.Sprintf("r%d", i)
		h := record.Header{Epoch: "e", Info: record.Info{ID: id, UpdatedAt: base.Add(time.Duration(i) * time.Minute)}}
		if err := st.Create(ctx, id, []record.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, h); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	q := store.ListQuery{Limit: 2}
	pages := 0
	for {
		page, err := st.List(ctx, q)
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
	if _, err := st.List(ctx, store.ListQuery{Cursor: "not a cursor"}); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("List with a foreign Cursor = %v, want invalid_argument", err)
	}
	// A listing with no Limit is a page of the store's own size.
	page, err := st.List(ctx, store.ListQuery{})
	if err != nil || len(page.Infos) != 5 || page.Next != "" {
		t.Fatalf("List with no Limit = %d Infos, Next %q (%v), want all 5 and no Next", len(page.Infos), page.Next, err)
	}
}

// A store times its Liveness rows by the clock it was given.
func TestStoresTimeLivenessByTheirClock(t *testing.T) {
	ctx := context.Background()
	var clock fakeClock
	files, err := store.NewFileStore(t.TempDir(), store.WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]store.Liveness{"memory": store.NewMemoryStore(store.WithClock(clock.now)), "file": files} {
		id := "instance-" + name
		if err := st.Beat(ctx, store.InstanceRow{ID: id}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if state, err := st.Instance(ctx, id); err != nil || !state.Alive {
			t.Fatalf("%s: a fresh row = %+v, %v, want alive", name, state, err)
		}
		clock.advance(2 * time.Minute)
		if state, err := st.Instance(ctx, id); err != nil || state.Alive {
			t.Fatalf("%s: a row past its ttl = %+v, %v, want not alive", name, state, err)
		}
		if err := st.Beat(ctx, store.InstanceRow{ID: id}, time.Minute); !errors.Is(err, store.ErrExpired) {
			t.Fatalf("%s: Beat of an expired row = %v, want ErrExpired", name, err)
		}
	}
}
