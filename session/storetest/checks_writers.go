package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jinhuang712/gotato/session"
)

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

// checkEpochBeforePosition checks that an Append of a writer of a deleted and
// re-created Record is ErrNotFound also when its position is wrong too.
func checkEpochBeforePosition(ctx context.Context, open Open) error {
	if _, err := create(ctx, open(), "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	stale := summary("r1", 9, "2026-10-17T00:00:09.000Z")
	stale.RecordEpoch = "epoch-other"
	err := open().Append(ctx, "r1", 8, []session.Entry{fact(9, "x")}, stale)
	if !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("an Append naming another epoch at a position that is not the head returned %v, want ErrNotFound: the epoch is checked before the position", err)
	}
	return nil
}

// checkConcurrentCreate checks that of Instances creating one ID at once,
// exactly one wins and the others get ErrExists, and the winner's Record stays.
func checkConcurrentCreate(ctx context.Context, open Open) error {
	const creators = 8
	errs := make([]error, creators)
	var wg sync.WaitGroup
	for i := range creators {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writer := fmt.Sprintf("creator-%d", i)
			info := summary("r1", 2, "2026-10-17T00:00:02.000Z")
			info.RecordEpoch = "epoch-" + writer
			errs[i] = open().Create(ctx, "r1", []session.Entry{checkpoint(1, writer), fact(2, writer)}, info)
		}(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner >= 0:
			return fmt.Errorf("creators %d and %d both created r1: Create of a taken ID is ErrExists, so one of them must get it", winner, i)
		case err == nil:
			winner = i
		case !errors.Is(err, session.ErrExists):
			return fmt.Errorf("creator %d that lost the race returned %v, want ErrExists", i, err)
		}
	}
	if winner < 0 {
		return errors.New("no creator won: one Create of an unused ID must succeed")
	}
	loaded, err := open().Load(ctx, "r1")
	if err != nil || loaded.Info.RecordEpoch != fmt.Sprintf("epoch-creator-%d", winner) {
		return fmt.Errorf("the Record after the race has epoch %q (%v), want the winner's: %s", loaded.Info.RecordEpoch, err, ruleAtomic)
	}
	return nil
}
