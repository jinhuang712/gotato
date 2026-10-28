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
	{"Create then Load returns the facts and Log returns every Entry", checkCreateLoad},
	{"Create of a taken ID is ErrExists", checkCreateTwice},
	{"RecordInfo keeps pending notices and the last run end through Create, Append, Load and List", checkInfoNotices},
	{"Append adds Entries after the head, seen by Log and Load", checkAppend},
	{"Load starts at the last Checkpoint", checkLastCheckpoint},
	{"Load returns the segment_closed Entries from before the last Checkpoint apart", checkSegments},
	{"a large log Entry is kept whole, read by Log, and left out of Load", checkSpilledEntry},
	{"a stale writer is refused with ErrConflict and writes nothing", checkStaleWriter},
	{"Append must continue the head, or it is refused", checkContinuesHead},
	{"of writers racing for one position, exactly one wins", checkRace},
	{"Log returns at most limit Entries, and every Entry when limit is 0 or less", checkLogLimit},
	{"List returns the last RecordInfo of each Record, newest update first", checkList},
	{"RecordInfo.RecordEpoch is kept by Create and Append and returned by Load and List", checkRecordEpoch},
	{"Delete removes a Record, and Delete of a missing one is ErrNotFound", checkDelete},
	{"Append by a writer of a deleted and re-created Record is ErrNotFound", checkDeletedWriter},
	{"Load, Log and Append of an unknown Record are ErrNotFound", checkUnknown},
	{"a canceled context stops Append and Load and writes nothing", checkCanceled},
	{"Beat creates a Liveness row that Instance returns, and ttl 0 ends it", checkLiveness},
	{"an expired Liveness row is not alive, and Beat on it is ErrExpired", checkExpiry},
	{"a row ended with ttl 0 is removed, for every handle", checkEndedRemoved},
	{"an ID the Store cannot keep is refused with ErrInvalidID by every call that takes an ID", checkInvalidID},
	{"IDs that differ only in case or Unicode form are different Records", checkIDCase},
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
	return entry(seq, gotato.EventKindSessionCreated, session.LayerFact, writer)
}

// compaction is a later Checkpoint: a compaction_ended Event.
func compaction(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.EventKindCompactionEnded, session.LayerFact, writer)
}

func fact(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.EventKindMessageCommitted, session.LayerFact, writer)
}

func logEntry(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.EventKindTurnStarted, session.LayerLog, writer)
}

func summary(id string, seq uint64, updated string) session.RecordInfo {
	return session.RecordInfo{ID: id, CreatedAt: infoTime("2026-10-17T00:00:00.000Z"), UpdatedAt: infoTime(updated), Messages: int(seq), Seq: seq, Metadata: map[string]string{"k": "v"}}
}

// infoTime reads a time written in RFC 3339; empty is the zero time.
func infoTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return at
}

func seqs(entries []session.Entry) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}

// The rules a failure names, so that an adapter author reads what to fix and
// not only what differed.
const (
	ruleLog       = "Log must return every Entry, facts and log, in order, unchanged"
	ruleLogLimit  = "Log must return at most limit Entries, from the first one after the position, and every Entry when limit is 0 or less"
	ruleLoadFacts = "Load must return the Entries of Layer fact only, from the last Checkpoint on, in order, unchanged"
	ruleSegments  = "Load must return the segment_closed Entries from before the last Checkpoint in Loaded.Segments, and Entries from the Checkpoint on stay facts"
	ruleAtomic    = "a refused write must leave the Record as it was, and a write is all or nothing"
)

// failf builds a failure message. It starts with the name of the call that
// misbehaved, which is why it is not an fmt.Errorf: an error string does not
// start with a capital letter, but a method name does.
func failf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// sameEntries compares what a call returned (named by what) with the Entries
// wanted, and names the rule when they differ.
func sameEntries(what string, got, want []session.Entry, rule string) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s returned %v, want %v: %s", what, seqs(got), seqs(want), rule)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Seq != w.Seq || g.Kind != w.Kind || g.Layer != w.Layer || !g.At.Equal(w.At) || g.Instance != w.Instance || string(g.Data) != string(w.Data) {
			return fmt.Errorf("%s returned Entry %d as %+v (data %s), want %+v (data %s): the store must keep Seq, Kind, Layer, At, Instance and Data unchanged", what, w.Seq, g, g.Data, w, w.Data)
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
	if err := sameEntries("Load after Create", loaded.Facts, entries[:2], ruleLoadFacts); err != nil {
		return err
	}
	if loaded.Head != 3 || loaded.Info.Seq != 3 || loaded.Info.Metadata["k"] != "v" {
		return failf("Load after Create returned head %d and RecordInfo %+v, want head 3 (the last Entry's Seq, log Entries included) and the RecordInfo written", loaded.Head, loaded.Info)
	}
	all, err := open().Log(ctx, "r1", 0, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries("Log after Create", all, entries, ruleLog)
}

// checkInfoNotices proves a store keeps the whole RecordInfo: the notices that
// wait for the next Run and the end of the last Run survive Create, Append, Load
// and List, so an adapter that copies RecordInfo field by field fails here and not
// by losing a notice in production.
func checkInfoNotices(ctx context.Context, open Open) error {
	store := open()
	pending := func(n uint64, text string) gotato.PendingNotice {
		return gotato.PendingNotice{MessageID: gotato.MessageID(fmt.Sprintf("m%d", n)), Kind: "order_update", Source: gotato.NoticeSourceService, Text: text, Number: n}
	}
	first := summary("r1", 3, "2026-10-17T00:00:03.000Z")
	first.LastRunEnd = infoTime("2026-10-17T00:00:02.500Z")
	first.Notices = []gotato.PendingNotice{pending(3, "one")}
	if err := store.Create(ctx, "r1", []session.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}, first); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	same := func(got session.RecordInfo, want session.RecordInfo, step string) error {
		if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
			return fmt.Errorf("%s returned RecordInfo with CreatedAt %v and UpdatedAt %v, want %v and %v: a store must keep the whole RecordInfo", step, got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
		}
		if !got.LastRunEnd.Equal(want.LastRunEnd) || len(got.Notices) != len(want.Notices) {
			return fmt.Errorf("%s returned RecordInfo with LastRunEnd %v and Notices %+v, want %v and %+v: a store must keep the whole RecordInfo, not copy it field by field", step, got.LastRunEnd, got.Notices, want.LastRunEnd, want.Notices)
		}
		for i := range want.Notices {
			if got.Notices[i] != want.Notices[i] {
				return fmt.Errorf("%s returned notice %d as %+v, want %+v: a store must keep the Notices in RecordInfo unchanged", step, i, got.Notices[i], want.Notices[i])
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
	second.LastRunEnd = infoTime("2026-10-17T00:00:04.000Z")
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
		return failf("List returned %v, %v, want the one Record: List returns the last RecordInfo of each Record", list, err)
	}
	return same(list[0], second, "List")
}

func checkCreateTwice(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err := create(ctx, open(), "r1"); !errors.Is(err, session.ErrExists) {
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
	more := []session.Entry{fact(4, "a"), logEntry(5, "a")}
	if err := store.Append(ctx, "r1", 3, more, summary("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
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
	if loaded.Head != 5 || loaded.Info.Seq != 5 {
		return failf("Load after Append returned head %d and RecordInfo.Seq %d, want 5 and 5: Append must move the head to its last Entry", loaded.Head, loaded.Info.Seq)
	}
	return sameEntries("Load after Append", loaded.Facts, []session.Entry{all[0], all[1], all[3]}, ruleLoadFacts)
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
		return failf("Load returned head %d, want 6: Head is the last Entry's Seq", loaded.Head)
	}
	return sameEntries("Load after a compaction_ended Entry", loaded.Facts, []session.Entry{more[0], more[2]}, "Load must start at the last Checkpoint (session_created or compaction_ended) and return its Layer fact Entries only")
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
	spilled := session.Entry{Seq: 4, Kind: gotato.EventKindToolResultSpilled, Layer: session.LayerLog, At: time.Date(2026, 10, 17, 0, 0, 4, 0, time.UTC), Data: body}
	after := fact(5, "a")
	more := []session.Entry{spilled, after}
	if err := store.Append(ctx, "r1", 3, more, summary("r1", 5, "2026-10-17T00:00:05.000Z")); err != nil {
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
	if err := sameEntries("Load over a megabyte log Entry", loaded.Facts, []session.Entry{entries[0], entries[1], after}, ruleLoadFacts+", so a load never reads a spilled body"); err != nil {
		return err
	}
	return nil
}

func segment(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.EventKindSegmentClosed, session.LayerFact, writer)
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
		return failf("Load from the first Checkpoint returned Loaded.Segments %v, want none: %s", seqs(loaded.Segments), ruleSegments)
	}
	if err := sameEntries("Load from the first Checkpoint", loaded.Facts, []session.Entry{first[0], first[1], first[2]}, ruleSegments); err != nil {
		return err
	}
	more := []session.Entry{segment(5, "a"), compaction(6, "a"), segment(7, "a"), fact(8, "a")}
	if err := store.Append(ctx, "r1", 4, more, summary("r1", 8, "2026-10-17T00:00:08.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load after a compaction: %w", err)
	}
	if err := sameEntries("Loaded.Segments after a compaction", loaded.Segments, []session.Entry{first[1], more[0]}, ruleSegments); err != nil {
		return err
	}
	return sameEntries("Loaded.Facts after a compaction", loaded.Facts, []session.Entry{more[1], more[2], more[3]}, ruleSegments)
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
		return fmt.Errorf("an Append for a position another writer already wrote returned %v, want ErrConflict: a position is written once", err)
	}
	got, err := open().Log(ctx, "r1", 3, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries("Log after the refused Append", got, winner, ruleAtomic)
}

func checkContinuesHead(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := store.Append(ctx, "r1", 4, []session.Entry{fact(5, "a")}, summary("r1", 5, "")); err == nil {
		return errors.New("an Append with after 4 to a Record whose head is 3 succeeded, want an error: a write must start at the head")
	}
	if err := store.Append(ctx, "r1", 3, []session.Entry{fact(5, "a")}, summary("r1", 5, "")); err == nil {
		return errors.New("an Append whose first Entry has Seq 5 after 3 succeeded, want an error: a write's Entries must have the Seqs after+1, after+2 and so on")
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if loaded.Head != 3 {
		return fmt.Errorf("a refused Append moved the head to %d, want 3: %s", loaded.Head, ruleAtomic)
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
			return fmt.Errorf("writers %d and %d both wrote position 4: a position is written once, so one of them must get ErrConflict", winner, i)
		case err == nil:
			winner = i
		case !errors.Is(err, session.ErrConflict):
			return fmt.Errorf("writer %d that lost the race for position 4 returned %v, want ErrConflict", i, err)
		}
	}
	if winner < 0 {
		return errors.New("no writer won position 4: of concurrent Appends at the same head, one must succeed")
	}
	got, err := open().Log(ctx, "r1", 3, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	writer := fmt.Sprintf("writer-%d", winner)
	return sameEntries("Log after the race", got, []session.Entry{fact(4, writer), fact(5, writer)}, "the Entries of the writer that won must all be stored, and none of the others: "+ruleAtomic)
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
		return failf("Load after Create returned RecordEpoch %q, want %q: a store must keep RecordInfo.RecordEpoch", loaded.Info.RecordEpoch, "epoch-1")
	}
	next := summary("epoch", 2, "2026-10-17T00:00:02.000Z")
	next.RecordEpoch = "epoch-1"
	if err := store.Append(ctx, "epoch", 1, []session.Entry{fact(2, "a")}, next); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	loaded, err = open().Load(ctx, "epoch")
	if err != nil || loaded.Info.RecordEpoch != "epoch-1" {
		return failf("Load after Append returned RecordEpoch %q (%v), want %q: Append must keep RecordInfo.RecordEpoch", loaded.Info.RecordEpoch, err, "epoch-1")
	}
	list, err := open().List(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for _, item := range list {
		if item.ID == "epoch" && item.RecordEpoch != "epoch-1" {
			return failf("List returned RecordEpoch %q, want %q: List must return the whole RecordInfo", item.RecordEpoch, "epoch-1")
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
		return failf("List returned %+v, want the last RecordInfo of each Record (older at Seq 2, newer), the most recently updated first (UpdatedAt)", list)
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
		return failf("Load after Delete returned %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "r1", 3, []session.Entry{fact(4, "a")}, summary("r1", 4, "")); !errors.Is(err, session.ErrNotFound) {
		return failf("Append after Delete returned %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "r1"); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("a second Delete returned %v, want ErrNotFound: Delete of a missing Record is an error, not a no-op", err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 0 {
		return failf("List after Delete returned %+v, %v, want no Record: Delete must remove the whole Record", list, err)
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
		return fmt.Errorf("an Append by the writer of the deleted Record, at the new Record's length, returned %v, want ErrNotFound: Append must compare RecordInfo.RecordEpoch before the position", err)
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil || loaded.Head != 4 || loaded.Info.RecordEpoch != "epoch-new" {
		return fmt.Errorf("the new Record after the refused Append has head %d and RecordEpoch %q (%v), want head 4 and epoch-new: %s", loaded.Head, loaded.Info.RecordEpoch, err, ruleAtomic)
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
		return fmt.Errorf("an Append by the writer of the deleted Record, over a position the new Record holds, returned %v, want ErrNotFound: a mismatch of RecordEpoch is ErrNotFound, not ErrConflict, and it is checked before the position", err)
	}
	log, err := open().Log(ctx, "r1", 0, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return sameEntries("Log after the refused Append", log, longer, "the refused Append must not write any Entry into the new Record, and "+ruleLog)
}

func checkUnknown(ctx context.Context, open Open) error {
	store := open()
	if _, err := store.Load(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		return failf("Load of an unknown ID returned %v, want ErrNotFound", err)
	}
	if _, err := store.Log(ctx, "missing", 0, 0); !errors.Is(err, session.ErrNotFound) {
		return failf("Log of an unknown ID returned %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "missing", 1, []session.Entry{fact(2, "a")}, summary("missing", 2, "")); !errors.Is(err, session.ErrNotFound) {
		return failf("Append to an unknown ID returned %v, want ErrNotFound", err)
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
		return failf("Append with a canceled context succeeded, want an error: every call must honor ctx")
	}
	if _, err := store.Load(canceled, "r1"); err == nil {
		return failf("Load with a canceled context succeeded, want an error: every call must honor ctx")
	}
	loaded, err := store.Load(ctx, "r1")
	if err != nil || loaded.Head != 3 {
		return fmt.Errorf("after an Append with a canceled context the head is %d (%v), want 3: a canceled call must write nothing", loaded.Head, err)
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
		return failf("Instance after Beat returned %+v, %v, want alive and draining: Beat must store the whole InstanceRow", state, err)
	}
	if state.Address != "http://a.example:8080" {
		return failf("Instance returned Address %q, want the one its row advertised: Beat must store InstanceRow.Address", state.Address)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-a"}, 0); err != nil {
		return failf("Beat with ttl 0: %w", err)
	}
	if state, err := open().Instance(ctx, "instance-a"); err != nil || state.Alive {
		return failf("Instance after Beat with ttl 0 returned %+v, %v, want not alive: ttl 0 ends the row", state, err)
	}
	if state, err := open().Instance(ctx, "never-seen"); err != nil || state.Alive {
		return failf("Instance of an ID never seen returned %+v, %v, want not alive and no error", state, err)
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
		return failf("Instance after its ttl returned %+v, %v, want not alive: a row expires by the store's clock", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-b"}, time.Hour); !errors.Is(err, session.ErrExpired) {
		return failf("Beat after the row's ttl returned %v, want ErrExpired: an expired row must stay, so that it never comes back", err)
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
		return failf("Instance after Beat returned %+v, %v, want alive", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c"}, 0); err != nil {
		return failf("Beat with ttl 0: %w", err)
	}
	if state, err := reader.Instance(ctx, "instance-c"); err != nil || state.Alive || state.Draining {
		return failf("Instance after Beat with ttl 0 returned %+v, %v, want not alive, from every handle: ttl 0 removes the row", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c"}, 0); err != nil {
		return failf("Beat with ttl 0 on a removed row returned %v, want nil", err)
	}
	if err := open().Beat(ctx, session.InstanceRow{ID: "instance-c"}, time.Hour); err != nil {
		return failf("Beat after the row was removed returned %v, want a new row: ttl 0 must leave nothing of the old row, so no ErrExpired", err)
	}
	return nil
}

// checkInvalidID holds a store to one rule for an ID it cannot keep: an empty
// one, one with a path separator, a "..", a leading ".", a control character,
// invalid UTF-8, or more than 100 bytes. Every call that takes it fails with
// ErrInvalidID (invalid_argument), never with ErrNotFound, so a Session fails
// the same way on every store.
func checkInvalidID(ctx context.Context, open Open) error {
	store := open()
	for _, id := range []string{"", "a/b", `a\b`, "..", "a..b", ".hidden", "a\x00b", "a\nb", "a\xffb", strings.Repeat("x", 101)} {
		_, loadErr := store.Load(ctx, id)
		_, logErr := store.Log(ctx, id, 0, 0)
		calls := []error{
			store.Create(ctx, id, []session.Entry{checkpoint(1, "a")}, summary(id, 1, "")),
			store.Append(ctx, id, 1, []session.Entry{fact(2, "a")}, summary(id, 2, "")),
			loadErr,
			logErr,
			store.Delete(ctx, id),
			store.Beat(ctx, session.InstanceRow{ID: id}, time.Hour),
		}
		for i, err := range calls {
			if !errors.Is(err, session.ErrInvalidID) || !gotato.IsCode(err, gotato.CodeInvalidArgument) {
				return fmt.Errorf("call %d with the ID %q = %v, want ErrInvalidID (invalid_argument)", i, id, err)
			}
		}
	}
	return nil
}

// checkIDCase checks that IDs differ by their characters alone: "Case" and
// "case" are two Records whatever the case rules of a file system, and so are
// the two spellings of one accented letter.
func checkIDCase(ctx context.Context, open Open) error {
	store := open()
	ids := []string{"Case", "case", "!case", "café", "café"}
	for _, id := range ids {
		if _, err := store.Load(ctx, id); !errors.Is(err, session.ErrNotFound) {
			return fmt.Errorf("load of %q before it exists = %v, want ErrNotFound", id, err)
		}
		if err := store.Create(ctx, id, []session.Entry{checkpoint(1, id)}, summary(id, 1, "")); err != nil {
			return fmt.Errorf("create %q: %w", id, err)
		}
	}
	for _, id := range ids {
		log, err := open().Log(ctx, id, 0, 0)
		if err != nil {
			return fmt.Errorf("log of %q: %w", id, err)
		}
		if err := sameEntries(fmt.Sprintf("Log of %q", id), log, []session.Entry{checkpoint(1, id)}, ruleLog); err != nil {
			return err
		}
	}
	if err := store.Delete(ctx, "case"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != len(ids)-1 {
		return fmt.Errorf("list after deleting one of %d = %d Records, %v", len(ids), len(list), err)
	}
	listed := map[string]bool{}
	for _, info := range list {
		listed[info.ID] = true
	}
	if listed["case"] || !listed["Case"] {
		return fmt.Errorf("list after deleting %q = %v", "case", listed)
	}
	if _, err := store.Load(ctx, "Case"); err != nil {
		return fmt.Errorf("load of %q after deleting %q: %w", "Case", "case", err)
	}
	return nil
}

// checkLogLimit holds a Store to Log's limit: at most limit Entries, from the
// first after the position, and every Entry when limit is 0.
func checkLogLimit(ctx context.Context, open Open) error {
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
	for _, c := range []struct {
		after uint64
		limit int
		want  []session.Entry
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
