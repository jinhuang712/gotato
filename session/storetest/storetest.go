// Package storetest checks that a session.Store keeps the contract Gotato
// relies on: a Record is an append-only log, every write is all or nothing,
// a position is written once, a stale writer is refused, a load starts at the
// last Checkpoint (a session_created or compaction_ended Entry, or the
// "checkpoint" of the earlier format) and hands back the segment_closed
// Entries before it, Liveness rows expire by the store's clock, and a row its
// Instance ended is removed.
//
// An adapter runs it from its own tests:
//
//	func TestMyStore(t *testing.T) {
//		storetest.Run(t, func() (storetest.Open, func(), error) {
//			db := newTestDatabase(t)
//			return func() session.Store { return mystore.New(db) }, db.Close, nil
//		})
//	}
//
// It is a package of its own so that importing testkit does not link the
// testing package into a program.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Open returns a new handle on one backing store. Every handle must see what
// the others wrote, as two Instances sharing one database do.
type Open func() session.Store

// Backing creates one new, empty backing store, returns how to open handles
// on it, and how to clean it up.
type Backing func() (Open, func(), error)

type check struct {
	name string
	run  func(ctx context.Context, open Open) error
}

var checks = []check{
	{"create then load", checkCreateLoad},
	{"create twice", checkCreateTwice},
	{"the summary keeps pending notices and the last run end", checkInfoNotices},
	{"append continues the log", checkAppend},
	{"load starts at the last checkpoint", checkLastCheckpoint},
	{"a load returns the segments stored before the last checkpoint", checkSegments},
	{"a large log entry is kept whole, read at its position, and not loaded", checkSpilledEntry},
	{"a stale writer is refused", checkStaleWriter},
	{"a write must continue the head", checkContinuesHead},
	{"one writer wins a position", checkRace},
	{"list returns summaries", checkList},
	{"the record epoch survives every read", checkRecordEpoch},
	{"delete", checkDelete},
	{"a writer of a deleted record is refused", checkDeletedWriter},
	{"an unknown record", checkUnknown},
	{"a canceled context", checkCanceled},
	{"liveness rows", checkLiveness},
	{"liveness rows expire", checkExpiry},
	{"an ended row is removed", checkEndedRemoved},
}

// Run runs every check as a subtest, each on a new backing store.
func Run(t *testing.T, backing Backing) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := runCheck(context.Background(), backing, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Check runs every check, each on a new backing store, and returns the
// problems it found, named by check.
func Check(ctx context.Context, backing Backing) []error {
	var problems []error
	for _, c := range checks {
		if err := runCheck(ctx, backing, c); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", c.name, err))
		}
	}
	return problems
}

func runCheck(ctx context.Context, backing Backing, c check) (err error) {
	open, cleanup, err := backing()
	if err != nil {
		return fmt.Errorf("backing: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return c.run(ctx, open)
}

// entry builds an Entry whose Data names its position, so a check can tell
// which writer's Entry a store kept.
func entry(seq uint64, kind gotato.EventKind, layer session.Layer, writer string) session.Entry {
	data, _ := json.Marshal(map[string]any{"seq": seq, "writer": writer})
	return session.Entry{Seq: seq, Kind: kind, Layer: layer, At: time.Date(2026, 10, 17, 0, 0, int(seq), 0, time.UTC), Data: data}
}

// checkpoint is the first Entry of a Record: a session_created Event.
func checkpoint(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.KindSessionCreated, session.LayerFact, writer)
}

// compaction is a later Checkpoint: a compaction_ended Event.
func compaction(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.KindCompactionEnded, session.LayerFact, writer)
}

func fact(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.KindMessageCommitted, session.LayerFact, writer)
}

func logEntry(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.KindTurnStarted, session.LayerLog, writer)
}

func summary(id string, seq uint64, updated string) session.RecordInfo {
	return session.RecordInfo{ID: id, CreatedAt: "2026-10-17T00:00:00.000Z", UpdatedAt: updated, Messages: int(seq), Seq: seq, Metadata: map[string]string{"k": "v"}}
}

func seqs(entries []session.Entry) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}

func sameEntries(got, want []session.Entry) error {
	if len(got) != len(want) {
		return fmt.Errorf("got entries %v, want %v", seqs(got), seqs(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Seq != w.Seq || g.Kind != w.Kind || g.Layer != w.Layer || !g.At.Equal(w.At) || g.Instance != w.Instance || string(g.Data) != string(w.Data) {
			return fmt.Errorf("entry %d = %+v (data %s), want %+v (data %s)", w.Seq, g, g.Data, w, w.Data)
		}
	}
	return nil
}

// create writes a Record with a checkpoint, a fact and a log Entry.
func create(ctx context.Context, store session.Store, id string) ([]session.Entry, error) {
	entries := []session.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}
	entries[1].Instance = "instance-a"
	return entries, store.Create(ctx, id, entries, summary(id, 3, "2026-10-17T00:00:03.000Z"))
}

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
	if err := sameEntries(loaded.Facts, entries[:2]); err != nil {
		return fmt.Errorf("load returns the facts only, unchanged: %w", err)
	}
	if loaded.Head != 3 || loaded.Info.Seq != 3 || loaded.Info.Metadata["k"] != "v" {
		return fmt.Errorf("load = head %d, summary %+v; want head 3 and the summary written", loaded.Head, loaded.Info)
	}
	all, err := open().Log(ctx, "r1", 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries(all, entries)
}

// checkInfoNotices proves a store keeps the whole RecordInfo: the notices that
// wait for the next Run and the end of the last Run survive Create, Append, Load
// and List, so an adapter that copies RecordInfo field by field fails here and not
// by losing a notice in production.
func checkInfoNotices(ctx context.Context, open Open) error {
	store := open()
	pending := func(n uint64, text string) gotato.PendingNotice {
		return gotato.PendingNotice{MessageID: gotato.MessageID(fmt.Sprintf("m%d", n)), Kind: "order_update", Source: gotato.NoticeFromService, Text: text, Number: n}
	}
	first := summary("r1", 3, "2026-10-17T00:00:03.000Z")
	first.LastRunEnd = "2026-10-17T00:00:02.500Z"
	first.Notices = []gotato.PendingNotice{pending(3, "one")}
	if err := store.Create(ctx, "r1", []session.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}, first); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	same := func(got session.RecordInfo, want session.RecordInfo, step string) error {
		if got.LastRunEnd != want.LastRunEnd || len(got.Notices) != len(want.Notices) {
			return fmt.Errorf("%s: summary has last_run_end %q and notices %+v, want %q and %+v", step, got.LastRunEnd, got.Notices, want.LastRunEnd, want.Notices)
		}
		for i := range want.Notices {
			if got.Notices[i] != want.Notices[i] {
				return fmt.Errorf("%s: notice %d = %+v, want %+v", step, i, got.Notices[i], want.Notices[i])
			}
		}
		return nil
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := same(loaded.Info, first, "after Create, Load"); err != nil {
		return err
	}
	second := summary("r1", 4, "2026-10-17T00:00:04.000Z")
	second.LastRunEnd = "2026-10-17T00:00:04.000Z"
	second.Notices = []gotato.PendingNotice{pending(3, "one"), pending(5, "two")}
	if err := store.Append(ctx, "r1", 3, []session.Entry{logEntry(4, "a")}, second); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := same(loaded.Info, second, "after Append, Load"); err != nil {
		return err
	}
	list, err := open().List(ctx)
	if err != nil || len(list) != 1 {
		return fmt.Errorf("list = %v, %v", list, err)
	}
	return same(list[0], second, "List")
}

func checkCreateTwice(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err := create(ctx, open(), "r1"); !errors.Is(err, session.ErrExists) {
		return fmt.Errorf("a second Create = %v, want ErrExists", err)
	}
	return nil
}

func checkAppend(ctx context.Context, open Open) error {
	store := open()
	entries, err := create(ctx, store, "r1")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	more := []session.Entry{fact(4, "a"), logEntry(5, "a")}
	if err := store.Append(ctx, "r1", 3, more, summary("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	all := append(entries, more...)
	got, err := open().Log(ctx, "r1", 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	if err := sameEntries(got, all); err != nil {
		return fmt.Errorf("log after Append: %w", err)
	}
	tail, err := open().Log(ctx, "r1", 3)
	if err != nil {
		return fmt.Errorf("log after 3: %w", err)
	}
	if err := sameEntries(tail, more); err != nil {
		return fmt.Errorf("log after 3: %w", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Head != 5 || loaded.Info.Seq != 5 {
		return fmt.Errorf("load after Append = head %d, summary seq %d; want 5", loaded.Head, loaded.Info.Seq)
	}
	return sameEntries(loaded.Facts, []session.Entry{all[0], all[1], all[3]})
}

func checkLastCheckpoint(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	more := []session.Entry{compaction(4, "a"), logEntry(5, "a"), fact(6, "a")}
	if err := store.Append(ctx, "r1", 3, more, summary("r1", 6, "2026-10-17T00:00:06.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Head != 6 {
		return fmt.Errorf("load head = %d, want 6", loaded.Head)
	}
	return sameEntries(loaded.Facts, []session.Entry{more[0], more[2]})
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
	spilled := session.Entry{Seq: 4, Kind: gotato.KindToolResultSpilled, Layer: session.LayerLog, At: time.Date(2026, 10, 17, 0, 0, 4, 0, time.UTC), Data: body}
	after := fact(5, "a")
	more := []session.Entry{spilled, after}
	if err := store.Append(ctx, "r1", 3, more, summary("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	got, err := open().Log(ctx, "r1", 3)
	if err != nil {
		return fmt.Errorf("log after 3: %w", err)
	}
	if err := sameEntries(got, more); err != nil {
		return fmt.Errorf("a megabyte Entry read back: %w", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := sameEntries(loaded.Facts, []session.Entry{entries[0], entries[1], after}); err != nil {
		return fmt.Errorf("a load reads the facts and not the spilled body: %w", err)
	}
	return nil
}

func segment(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.KindSegmentClosed, session.LayerFact, writer)
}

// checkSegments checks that a load hands back the segment_closed Entries from
// before the last Checkpoint apart from the facts, since the Checkpoint lists
// the Segments it uses by number and does not hold their Messages, and that a
// segment_closed Entry after the Checkpoint is an ordinary fact.
func checkSegments(ctx context.Context, open Open) error {
	store := open()
	first := []session.Entry{checkpoint(1, "a"), segment(2, "a"), fact(3, "a"), logEntry(4, "a")}
	if err := store.Create(ctx, "r1", first, summary("r1", 4, "2026-10-17T00:00:04.000Z")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if len(loaded.Segments) != 0 {
		return fmt.Errorf("a load from the first Checkpoint returns segments %v apart; the Entry after it is a fact", seqs(loaded.Segments))
	}
	if err := sameEntries(loaded.Facts, []session.Entry{first[0], first[1], first[2]}); err != nil {
		return fmt.Errorf("facts from the first Checkpoint: %w", err)
	}
	more := []session.Entry{segment(5, "a"), compaction(6, "a"), segment(7, "a"), fact(8, "a")}
	if err := store.Append(ctx, "r1", 4, more, summary("r1", 8, "2026-10-17T00:00:08.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load after a compaction: %w", err)
	}
	if err := sameEntries(loaded.Segments, []session.Entry{first[1], more[0]}); err != nil {
		return fmt.Errorf("segments before the last Checkpoint: %w", err)
	}
	return sameEntries(loaded.Facts, []session.Entry{more[1], more[2], more[3]})
}

func checkStaleWriter(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	first, second := open(), open()
	winner := []session.Entry{fact(4, "first")}
	if err := first.Append(ctx, "r1", 3, winner, summary("r1", 4, "2026-10-17T00:00:04.000Z")); err != nil {
		return fmt.Errorf("first Append: %w", err)
	}
	stale := []session.Entry{fact(4, "second"), fact(5, "second")}
	if err := second.Append(ctx, "r1", 3, stale, summary("r1", 5, "2026-10-17T00:00:05.000Z")); !errors.Is(err, session.ErrConflict) {
		return fmt.Errorf("an Append after a position already written = %v, want ErrConflict", err)
	}
	got, err := open().Log(ctx, "r1", 3)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries(got, winner)
}

func checkContinuesHead(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := store.Append(ctx, "r1", 4, []session.Entry{fact(5, "a")}, summary("r1", 5, "")); err == nil {
		return errors.New("an Append past the head succeeded")
	}
	if err := store.Append(ctx, "r1", 3, []session.Entry{fact(5, "a")}, summary("r1", 5, "")); err == nil {
		return errors.New("an Append whose first Entry is not after the head succeeded")
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Head != 3 {
		return fmt.Errorf("a refused Append moved the head to %d", loaded.Head)
	}
	return nil
}

func checkRace(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writer := fmt.Sprintf("writer-%d", i)
			errs[i] = open().Append(ctx, "r1", 3, []session.Entry{fact(4, writer), fact(5, writer)}, summary("r1", 5, ""))
		}(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner >= 0:
			return fmt.Errorf("writers %d and %d both wrote position 4", winner, i)
		case err == nil:
			winner = i
		case !errors.Is(err, session.ErrConflict):
			return fmt.Errorf("writer %d = %v, want ErrConflict", i, err)
		}
	}
	if winner < 0 {
		return errors.New("no writer won position 4")
	}
	got, err := open().Log(ctx, "r1", 3)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	writer := fmt.Sprintf("writer-%d", winner)
	return sameEntries(got, []session.Entry{fact(4, writer), fact(5, writer)})
}

// checkRecordEpoch: a store keeps RecordInfo.RecordEpoch with the rest of the
// RecordInfo, from Create through Append, and returns it from Load and List. A
// store that drops it would give a Record one epoch in its Entries and another
// in its RecordInfo, and every stream resume would be refused.
func checkRecordEpoch(ctx context.Context, open Open) error {
	store := open()
	first := summary("epoch", 1, "2026-10-17T00:00:01.000Z")
	first.RecordEpoch = "epoch-1"
	if err := store.Create(ctx, "epoch", []session.Entry{checkpoint(1, "a")}, first); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	loaded, err := open().Load(ctx, "epoch")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Info.RecordEpoch != "epoch-1" {
		return fmt.Errorf("load after create returns record epoch %q, want %q", loaded.Info.RecordEpoch, "epoch-1")
	}
	next := summary("epoch", 2, "2026-10-17T00:00:02.000Z")
	next.RecordEpoch = "epoch-1"
	if err := store.Append(ctx, "epoch", 1, []session.Entry{fact(2, "a")}, next); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "epoch")
	if err != nil || loaded.Info.RecordEpoch != "epoch-1" {
		return fmt.Errorf("load after append returns record epoch %q (%v), want %q", loaded.Info.RecordEpoch, err, "epoch-1")
	}
	list, err := open().List(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for _, item := range list {
		if item.ID == "epoch" && item.RecordEpoch != "epoch-1" {
			return fmt.Errorf("list returns record epoch %q, want %q", item.RecordEpoch, "epoch-1")
		}
	}
	return nil
}

func checkList(ctx context.Context, open Open) error {
	store := open()
	if err := store.Create(ctx, "older", []session.Entry{checkpoint(1, "a")}, summary("older", 1, "2026-10-17T00:00:01.000Z")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := store.Create(ctx, "newer", []session.Entry{checkpoint(1, "a")}, summary("newer", 1, "2026-10-17T00:00:02.000Z")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := store.Append(ctx, "older", 1, []session.Entry{fact(2, "a")}, summary("older", 2, "2026-10-17T00:00:03.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	list, err := open().List(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if len(list) != 2 || list[0].ID != "older" || list[0].Seq != 2 || list[1].ID != "newer" {
		return fmt.Errorf("list = %+v, want the last summary of each, the most recently updated first", list)
	}
	return nil
}

func checkDelete(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := open().Delete(ctx, "r1"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if _, err := store.Load(ctx, "r1"); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("load after Delete = %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "r1", 3, []session.Entry{fact(4, "a")}, summary("r1", 4, "")); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("append after Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "r1"); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("a second Delete = %v, want ErrNotFound", err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 0 {
		return fmt.Errorf("list after Delete = %+v, %v", list, err)
	}
	return nil
}

// checkDeletedWriter holds a store to the epoch in Append's RecordInfo: a writer
// of a Record that was deleted and created again under the same ID gets
// ErrNotFound, whether or not the new Record is as long as the old one was,
// and the new Record is left as it was.
func checkDeletedWriter(ctx context.Context, open Open) error {
	withEpoch := func(s session.RecordInfo, epoch string) session.RecordInfo {
		s.RecordEpoch = epoch
		return s
	}
	old := open()
	if err := old.Create(ctx, "r1", []session.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}, withEpoch(summary("r1", 3, "2026-10-17T00:00:03.000Z"), "epoch-old")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	// A write that lands warms any cache the handle keeps.
	if err := old.Append(ctx, "r1", 3, []session.Entry{fact(4, "a")}, withEpoch(summary("r1", 4, "2026-10-17T00:00:04.000Z"), "epoch-old")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	if err := open().Delete(ctx, "r1"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	// The new Record is as long as the old one: a length check passes.
	fresh := open()
	if err := fresh.Create(ctx, "r1", []session.Entry{checkpoint(1, "b"), fact(2, "b"), logEntry(3, "b")}, withEpoch(summary("r1", 3, "2026-10-17T00:00:05.000Z"), "epoch-new")); err != nil {
		return fmt.Errorf("create again: %w", err)
	}
	if err := fresh.Append(ctx, "r1", 3, []session.Entry{fact(4, "b")}, withEpoch(summary("r1", 4, "2026-10-17T00:00:06.000Z"), "epoch-new")); err != nil {
		return fmt.Errorf("append to the new record: %w", err)
	}
	err := old.Append(ctx, "r1", 4, []session.Entry{fact(5, "a")}, withEpoch(summary("r1", 5, "2026-10-17T00:00:07.000Z"), "epoch-old"))
	if !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("an append of the deleted record, at the new record's length = %v, want ErrNotFound", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil || loaded.Head != 4 || loaded.Info.Epoch() != "epoch-new" {
		return fmt.Errorf("the new record after the refused append: head %d, epoch %q, %v; want head 4 and epoch-new", loaded.Head, loaded.Info.Epoch(), err)
	}

	// The new Record is longer, written as one batch that covers the
	// position the old writer wants: it must not be written twice.
	if err := open().Delete(ctx, "r1"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if err := old.Create(ctx, "r1", []session.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}, withEpoch(summary("r1", 3, "2026-10-17T00:00:08.000Z"), "epoch-third")); err != nil {
		return fmt.Errorf("create a third time: %w", err)
	}
	if err := open().Delete(ctx, "r1"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	longer := []session.Entry{checkpoint(1, "c"), fact(2, "c"), logEntry(3, "c"), fact(4, "c")}
	if err := open().Create(ctx, "r1", longer, withEpoch(summary("r1", 4, "2026-10-17T00:00:09.000Z"), "epoch-fourth")); err != nil {
		return fmt.Errorf("create a fourth time: %w", err)
	}
	err = old.Append(ctx, "r1", 3, []session.Entry{fact(4, "a")}, withEpoch(summary("r1", 4, "2026-10-17T00:00:10.000Z"), "epoch-third"))
	if !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("an append of the deleted record, over a position the new record holds = %v, want ErrNotFound", err)
	}
	log, err := open().Log(ctx, "r1", 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries(log, longer)
}

func checkUnknown(ctx context.Context, open Open) error {
	store := open()
	if _, err := store.Load(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("load = %v, want ErrNotFound", err)
	}
	if _, err := store.Log(ctx, "missing", 0); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("log = %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "missing", 1, []session.Entry{fact(2, "a")}, summary("missing", 2, "")); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("append = %v, want ErrNotFound", err)
	}
	return nil
}

func checkCanceled(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Append(canceled, "r1", 3, []session.Entry{fact(4, "a")}, summary("r1", 4, "")); err == nil {
		return errors.New("append ignored a canceled context")
	}
	if _, err := store.Load(canceled, "r1"); err == nil {
		return errors.New("load ignored a canceled context")
	}
	loaded, err := store.Load(ctx, "r1")
	if err != nil || loaded.Head != 3 {
		return fmt.Errorf("an Append with a canceled context wrote: head %d, %v", loaded.Head, err)
	}
	return nil
}

func checkLiveness(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-a", Draining: true, Address: "http://a.example:8080"}, time.Hour); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	state, err := open().Instance(ctx, "instance-a")
	if err != nil || !state.Alive || !state.Draining {
		return fmt.Errorf("instance = %+v, %v; want alive and draining", state, err)
	}
	if state.Address != "http://a.example:8080" {
		return fmt.Errorf("instance address = %q, want the one its row advertised", state.Address)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-a"}, 0); err != nil {
		return fmt.Errorf("beat with ttl 0: %w", err)
	}
	if state, err := open().Instance(ctx, "instance-a"); err != nil || state.Alive {
		return fmt.Errorf("instance after its row ended = %+v, %v; want not alive", state, err)
	}
	if state, err := open().Instance(ctx, "never-seen"); err != nil || state.Alive {
		return fmt.Errorf("an unknown Instance = %+v, %v; want not alive", state, err)
	}
	return nil
}

func checkExpiry(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-b"}, 50*time.Millisecond); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	time.Sleep(120 * time.Millisecond)
	if state, err := open().Instance(ctx, "instance-b"); err != nil || state.Alive {
		return fmt.Errorf("instance after its ttl = %+v, %v; want not alive", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-b"}, time.Hour); !errors.Is(err, session.ErrExpired) {
		return fmt.Errorf("beat after its ttl = %v, want ErrExpired", err)
	}
	return nil
}

// checkEndedRemoved checks that a row its Instance ended, with a ttl of 0, is
// removed: from then on the Instance is not alive for any handle, so every
// Claim that names it is dead, and nothing of the row is left, so its ID is
// unknown again.
func checkEndedRemoved(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c", Draining: true}, time.Hour); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	reader := open()
	if state, err := reader.Instance(ctx, "instance-c"); err != nil || !state.Alive {
		return fmt.Errorf("instance = %+v, %v; want alive", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c"}, 0); err != nil {
		return fmt.Errorf("beat with ttl 0: %w", err)
	}
	if state, err := reader.Instance(ctx, "instance-c"); err != nil || state.Alive || state.Draining {
		return fmt.Errorf("instance after its row was removed = %+v, %v; want not alive", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c"}, 0); err != nil {
		return fmt.Errorf("beat with ttl 0 on a removed row = %v, want nil", err)
	}
	if err := open().Beat(ctx, session.InstanceRow{ID: "instance-c"}, time.Hour); err != nil {
		return fmt.Errorf("beat after the row was removed = %v; want a new row, since nothing of the old one is left", err)
	}
	return nil
}
