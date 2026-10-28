package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

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
