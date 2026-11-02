package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
)

const ruleOneRead = "Load must read the Record in one read: its Entries, its position and its Header all come from the same write"

func checkCreateLoad(ctx context.Context, open Open) error {
	store := open()
	entries, err := create(ctx, store, "r1")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	loaded, err := store.Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := sameEntries("Load after Create", loaded.Entries, entries[:2], ruleLoadFacts); err != nil {
		return err
	}
	if loaded.Seq != 3 || loaded.Header.Info.Metadata["k"] != "v" || loaded.Header.Info.Messages != 3 {
		return failf("Load after Create returned Seq %d and Header %+v, want Seq 3 (the last Entry's Seq, log Entries included) and the Header written", loaded.Seq, loaded.Header)
	}
	all, err := open().Log(ctx, "r1", 0, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries("Log after Create", all, entries, ruleLog)
}

// checkHeaderKept proves a store keeps the whole Header of the last write: the
// epoch, the listing columns and the State bytes, which no store reads, come
// back from Load as they were, after Create and after Append. An adapter that
// copies the Header field by field, or normalizes the State, fails here and
// not by losing a Run in flight in production.
func checkHeaderKept(ctx context.Context, open Open) error {
	store := open()
	first := header("r1", 3, "2026-10-17T00:00:03.000Z")
	first.State = json.RawMessage(`{"running":"run-1","busy":true,"notices":[{"number":3,"text":"one"}],"last_run_end":"2026-10-17T00:00:02.500Z"}`)
	if err := store.Create(ctx, "r1", []record.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}, first); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	same := func(got, want record.Header, step string) error {
		if got.Epoch != want.Epoch {
			return fmt.Errorf("%s returned Epoch %q, want %q: a store must keep the Header unchanged", step, got.Epoch, want.Epoch)
		}
		if !got.Info.CreatedAt.Equal(want.Info.CreatedAt) || !got.Info.UpdatedAt.Equal(want.Info.UpdatedAt) || got.Info.ID != want.Info.ID || got.Info.Messages != want.Info.Messages || got.Info.Metadata["k"] != want.Info.Metadata["k"] {
			return fmt.Errorf("%s returned Info %+v, want %+v: a store must keep the whole Info", step, got.Info, want.Info)
		}
		if string(got.State) != string(want.State) {
			return fmt.Errorf("%s returned State %s, want %s: a store must keep Header.State byte for byte, as it keeps an Entry's Data", step, got.State, want.State)
		}
		if got.LogFrom != want.LogFrom {
			return fmt.Errorf("%s returned LogFrom %d, want %d: a store must keep Header.LogFrom", step, got.LogFrom, want.LogFrom)
		}
		return nil
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := same(loaded.Header, first, "after Create, Load"); err != nil {
		return err
	}
	second := header("r1", 4, "2026-10-17T00:00:04.000Z")
	second.State = json.RawMessage(`{"busy":true,"notices":[{"number":3,"text":"one"},{"number":5,"text":"two"}],"runs":2}`)
	second.LogFrom = 1
	if err := store.Append(ctx, "r1", 3, []record.Entry{logEntry(4, "a")}, second); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	return same(loaded.Header, second, "after Append, Load")
}

func checkCreateTwice(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err := create(ctx, open(), "r1"); !errors.Is(err, store.ErrExists) {
		return fmt.Errorf("a second Create of the same ID returned %v, want ErrExists: Create must not overwrite a Record", err)
	}
	return nil
}

func checkAppend(ctx context.Context, open Open) error {
	store := open()
	entries, err := create(ctx, store, "r1")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	more := []record.Entry{fact(4, "a"), logEntry(5, "a")}
	if err := store.Append(ctx, "r1", 3, more, header("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	all := append(entries, more...)
	got, err := open().Log(ctx, "r1", 0, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	if err := sameEntries("Log after Append", got, all, ruleLog); err != nil {
		return err
	}
	tail, err := open().Log(ctx, "r1", 3, 0)
	if err != nil {
		return failf("Log after 3: %w", err)
	}
	if err := sameEntries("Log after 3", tail, more, "Log(after) must return the Entries with Seq above after, and every kind of them"); err != nil {
		return err
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Seq != 5 || loaded.Header.Info.Messages != 5 {
		return failf("Load after Append returned Seq %d and Info.Messages %d, want 5 and 5: Append must move the position to its last Entry and replace the Header", loaded.Seq, loaded.Header.Info.Messages)
	}
	return sameEntries("Load after Append", loaded.Entries, []record.Entry{all[0], all[1], all[3]}, ruleLoadFacts)
}

func checkLastCheckpoint(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	more := []record.Entry{compaction(4, "a"), logEntry(5, "a"), fact(6, "a")}
	if err := store.Append(ctx, "r1", 3, more, header("r1", 6, "2026-10-17T00:00:06.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Seq != 6 {
		return failf("Load returned Seq %d, want 6: Seq is the last Entry's Seq", loaded.Seq)
	}
	return sameEntries("Load after a compaction_ended Entry", loaded.Entries, []record.Entry{more[0], more[2]}, "Load must start at the last Checkpoint (session_created or compaction_ended) and return its Layer fact Entries only, when the Header has no LogFrom")
}

// checkLogFrom holds a store to the Header's LogFrom: a Load returns, besides
// the facts from the last Checkpoint, every Entry (log Entries too) from
// LogFrom on, each once and in order, and LogFrom is the one of the last
// write: a later write with none turns it off.
func checkLogFrom(ctx context.Context, open Open) error {
	store := open()
	all, err := create(ctx, store, "r1")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	const rule = ruleLoadFacts + "; Header.LogFrom asks for every Entry from that position, log Entries too, each once"
	// write appends more with the given LogFrom, then loads and compares.
	write := func(more []record.Entry, logFrom uint64, what string, want ...int) error {
		h := header("r1", more[len(more)-1].Seq, "2026-10-17T00:00:09.000Z")
		h.LogFrom = logFrom
		if err := store.Append(ctx, "r1", all[len(all)-1].Seq, more, h); err != nil {
			return fmt.Errorf("append: %w", err)
		}
		all = append(all, more...)
		loaded, err := open().Load(ctx, "r1")
		if err != nil {
			return fmt.Errorf("load: %w", err)
		}
		if loaded.Header.LogFrom != logFrom || loaded.Seq != all[len(all)-1].Seq {
			return failf("Load %s returned Header.LogFrom %d and Seq %d, want %d and %d", what, loaded.Header.LogFrom, loaded.Seq, logFrom, all[len(all)-1].Seq)
		}
		wanted := make([]record.Entry, len(want))
		for i, seq := range want {
			wanted[i] = all[seq-1]
		}
		return sameEntries("Load "+what, loaded.Entries, wanted, rule)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := sameEntries("Load of a Header with no LogFrom", loaded.Entries, all[:2], ruleLoadFacts); err != nil {
		return err
	}
	if err := write([]record.Entry{logEntry(4, "a"), fact(5, "a"), logEntry(6, "a")}, 3, "with a LogFrom at 3", 1, 2, 3, 4, 5, 6); err != nil {
		return err
	}
	// A Checkpoint after it: the facts start there, and LogFrom follows it.
	if err := write([]record.Entry{compaction(7, "a"), logEntry(8, "a"), fact(9, "a")}, 7, "with a LogFrom at a Checkpoint", 7, 8, 9); err != nil {
		return err
	}
	// A write with no LogFrom turns it off.
	return write([]record.Entry{logEntry(10, "a")}, 0, "after a write with no LogFrom", 7, 9)
}

// checkSpilledEntry checks the Entry of a spilled Tool Result (G-F35): a log
// Entry of a megabyte. A store keeps it whole, Log(after seq-1) returns it
// first, and a load reads facts only, so it never reads the body.
func checkSpilledEntry(ctx context.Context, open Open) error {
	store := open()
	entries, err := create(ctx, store, "r1")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"seq": 4, "writer": "a", "body": strings.Repeat("row,paid,41.20\n", 70000)})
	spilled := record.Entry{Seq: 4, Kind: gotato.EventKindToolResultSpilled, Layer: record.LayerLog, At: time.Date(2026, 10, 17, 0, 0, 4, 0, time.UTC), Data: body}
	after := fact(5, "a")
	more := []record.Entry{spilled, after}
	if err := store.Append(ctx, "r1", 3, more, header("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	got, err := open().Log(ctx, "r1", 3, 0)
	if err != nil {
		return failf("Log after 3: %w", err)
	}
	if err := sameEntries("Log after 3 over a megabyte Entry", got, more, "a log Entry of a megabyte must be kept whole and returned by Log at its position"); err != nil {
		return err
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := sameEntries("Load over a megabyte log Entry", loaded.Entries, []record.Entry{entries[0], entries[1], after}, ruleLoadFacts+", so a load never reads a spilled body"); err != nil {
		return err
	}
	return nil
}

// checkSegments checks that a load hands back the segment_closed Entries from
// before the last Checkpoint apart from the facts, since the Checkpoint lists
// the Segments it uses by number and does not hold their Messages, and that a
// segment_closed Entry after the Checkpoint is an ordinary fact.
func checkSegments(ctx context.Context, open Open) error {
	store := open()
	first := []record.Entry{checkpoint(1, "a"), segment(2, "a"), fact(3, "a"), logEntry(4, "a")}
	if err := store.Create(ctx, "r1", first, header("r1", 4, "2026-10-17T00:00:04.000Z")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if len(loaded.Segments) != 0 {
		return failf("Load from the first Checkpoint returned Loaded.Segments %v, want none: %s", seqs(loaded.Segments), ruleSegments)
	}
	if err := sameEntries("Load from the first Checkpoint", loaded.Entries, []record.Entry{first[0], first[1], first[2]}, ruleSegments); err != nil {
		return err
	}
	more := []record.Entry{segment(5, "a"), compaction(6, "a"), segment(7, "a"), fact(8, "a")}
	if err := store.Append(ctx, "r1", 4, more, header("r1", 8, "2026-10-17T00:00:08.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load after a compaction: %w", err)
	}
	if err := sameEntries("Loaded.Segments after a compaction", loaded.Segments, []record.Entry{first[1], more[0]}, ruleSegments); err != nil {
		return err
	}
	return sameEntries("Loaded.Entries after a compaction", loaded.Entries, []record.Entry{more[1], more[2], more[3]}, ruleSegments)
}

// checkLoadRacingAppends holds a store to the one read of a Load. A writer
// appends one fact at a time, and each write's Header names the position it
// leaves the Record at (Info.Messages, and "seq" in State); readers Load all
// the while. A Load whose Entries come from one write and whose Header, or
// position, comes from another, as one that reads in two steps does, fails.
func checkLoadRacingAppends(ctx context.Context, open Open) error {
	const writes = 120
	stateOf := func(seq uint64) record.Header {
		h := header("r1", seq, "2026-10-17T00:00:00.000Z")
		h.State = json.RawMessage(fmt.Sprintf(`{"seq":%d}`, seq))
		return h
	}
	if err := open().Create(ctx, "r1", []record.Entry{checkpoint(1, "w")}, stateOf(1)); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	var (
		wg      sync.WaitGroup
		done    = make(chan struct{})
		mu      sync.Mutex
		failure error
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if failure == nil {
			failure = err
		}
	}
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := open()
			for {
				select {
				case <-done:
					return
				default:
				}
				loaded, err := reader.Load(ctx, "r1")
				if err != nil {
					fail(fmt.Errorf("load: %w", err))
					return
				}
				var state struct {
					Seq uint64 `json:"seq"`
				}
				if err := json.Unmarshal(loaded.Header.State, &state); err != nil || len(loaded.Entries) == 0 {
					fail(fmt.Errorf("a Load returned State %s and %d Entries (%v): %s", loaded.Header.State, len(loaded.Entries), err, ruleOneRead))
					return
				}
				last := loaded.Entries[len(loaded.Entries)-1].Seq
				if state.Seq != loaded.Seq || last != loaded.Seq || uint64(loaded.Header.Info.Messages) != loaded.Seq {
					fail(fmt.Errorf("a Load returned Seq %d, a last Entry at %d, and a Header written at %d (Info.Messages %d): %s", loaded.Seq, last, state.Seq, loaded.Header.Info.Messages, ruleOneRead))
					return
				}
			}
		}()
	}
	writer := open()
	var writeErr error
	for seq := uint64(2); seq <= writes && writeErr == nil; seq++ {
		writeErr = writer.Append(ctx, "r1", seq-1, []record.Entry{fact(seq, "w")}, stateOf(seq))
		time.Sleep(50 * time.Microsecond)
	}
	close(done)
	wg.Wait()
	if writeErr != nil {
		return fmt.Errorf("append: %w", writeErr)
	}
	return failure
}

// checkLogLimit holds a store to Log's limit: at most limit Entries, from the
// first after the position, and every Entry when limit is 0.
func checkLogLimit(ctx context.Context, open Open) error {
	store := open()
	entries, err := create(ctx, store, "r1")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	more := []record.Entry{fact(4, "a"), logEntry(5, "a")}
	if err := store.Append(ctx, "r1", 3, more, header("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	all := append(entries, more...)
	for _, c := range []struct {
		after uint64
		limit int
		want  []record.Entry
	}{
		{0, 1, all[:1]},
		{0, 4, all[:4]},
		{2, 2, all[2:4]},
		{3, 1, all[3:4]},
		{4, 10, all[4:]},
		{5, 1, nil},
		{0, 0, all},
		{0, -1, all},
	} {
		got, err := open().Log(ctx, "r1", c.after, c.limit)
		if err != nil {
			return fmt.Errorf("log after %d, limit %d: %w", c.after, c.limit, err)
		}
		if err := sameEntries(fmt.Sprintf("Log after %d, limit %d", c.after, c.limit), got, c.want, ruleLogLimit); err != nil {
			return err
		}
	}
	return nil
}

// checkLogPastHead checks that Log after the head, or far past it, is an
// empty answer and not an error.
func checkLogPastHead(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	for _, after := range []uint64{3, 4, 1000} {
		got, err := open().Log(ctx, "r1", after, 0)
		if err != nil || len(got) != 0 {
			return failf("Log after %d of a Record whose head is 3 returned %d Entries, %v, want none and no error", after, len(got), err)
		}
	}
	return nil
}
