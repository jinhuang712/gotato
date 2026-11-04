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
	return sameEntries("Load after a compaction_ended Entry", loaded.Entries, []record.Entry{more[0], more[2]}, "Load must start at the last Checkpoint (session_created or compaction_ended) and return its Layer fact Entries only")
}

// checkSpilledEntry checks the Entry of a spilled Tool Result (F6.7): a log
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
// leaves the Record at (Info.Messages); readers Load all the while. A Load whose Entries come from one write and whose Header, or
// position, comes from another, as one that reads in two steps does, fails.
func checkLoadRacingAppends(ctx context.Context, open Open) error {
	const writes = 120
	if err := open().Create(ctx, "r1", []record.Entry{checkpoint(1, "w")}, header("r1", 1, "2026-10-17T00:00:00.000Z")); err != nil {
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
				if len(loaded.Entries) == 0 {
					fail(fmt.Errorf("a Load returned no Entries: %s", ruleOneRead))
					return
				}
				last := loaded.Entries[len(loaded.Entries)-1].Seq
				if last != loaded.Seq || uint64(loaded.Header.Info.Messages) != loaded.Seq {
					fail(fmt.Errorf("a Load returned Seq %d, a last Entry at %d, and a Header written at %d (Info.Messages): %s", loaded.Seq, last, loaded.Header.Info.Messages, ruleOneRead))
					return
				}
			}
		}()
	}
	writer := open()
	var writeErr error
	for seq := uint64(2); seq <= writes && writeErr == nil; seq++ {
		writeErr = writer.Append(ctx, "r1", seq-1, []record.Entry{fact(seq, "w")}, header("r1", seq, "2026-10-17T00:00:00.000Z"))
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
