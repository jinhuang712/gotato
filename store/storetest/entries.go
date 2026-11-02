package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
)

// entry builds an Entry whose Data names its position, so a check can tell
// which writer's Entry a store kept.
func entry(seq uint64, kind gotato.EventKind, layer record.Layer, writer string) record.Entry {
	data, _ := json.Marshal(map[string]any{"seq": seq, "writer": writer})
	return record.Entry{Seq: seq, Kind: kind, Layer: layer, At: time.Date(2026, 10, 17, 0, 0, int(seq), 0, time.UTC), Data: data}
}

// checkpoint is the first Entry of a Record: a session_created Event.
func checkpoint(seq uint64, writer string) record.Entry {
	return entry(seq, gotato.EventKindSessionCreated, record.LayerFact, writer)
}

// compaction is a later Checkpoint: a compaction_ended Event.
func compaction(seq uint64, writer string) record.Entry {
	return entry(seq, gotato.EventKindCompactionEnded, record.LayerFact, writer)
}

func fact(seq uint64, writer string) record.Entry {
	return entry(seq, gotato.EventKindMessageCommitted, record.LayerFact, writer)
}

func logEntry(seq uint64, writer string) record.Entry {
	return entry(seq, gotato.EventKindTurnStarted, record.LayerLog, writer)
}

func segment(seq uint64, writer string) record.Entry {
	return entry(seq, gotato.EventKindSegmentClosed, record.LayerFact, writer)
}

// header is the Header of a write of Record id that leaves it at seq: its
// Epoch is "epoch-"+id, its Info the listing columns, and its State bytes of
// Gotato's own that no store reads and every store must give back as they were.
func header(id string, seq uint64, updated string) record.Header {
	state, _ := json.Marshal(map[string]any{"seq": seq, "running": "run-1", "notices": []string{"one", "two"}})
	return record.Header{
		Epoch: "epoch-" + id,
		Info: record.Info{
			ID: id, CreatedAt: infoTime("2026-10-17T00:00:00.000Z"), UpdatedAt: infoTime(updated),
			Messages: int(seq), Metadata: map[string]string{"k": "v"},
		},
		State: state,
	}
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

func seqs(entries []record.Entry) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}

// create writes a Record with a checkpoint, a fact and a log Entry.
func create(ctx context.Context, st store.Records, id string) ([]record.Entry, error) {
	entries := []record.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}
	entries[1].Instance = "instance-a"
	return entries, st.Create(ctx, id, entries, header(id, 3, "2026-10-17T00:00:03.000Z"))
}

// listerOf returns the handle as a Lister, or says what the Lister suite needs.
func listerOf(open Open) (store.Records, store.Lister, error) {
	handle := open()
	lister, ok := handle.(store.Lister)
	if !ok {
		return nil, nil, errors.New("the Records the backing opens is not a store.Lister: RunLister checks a store that lists")
	}
	return handle, lister, nil
}

// listAll follows the Next of every page, at limit a page, and returns the
// Infos in the order they came. A page with no Infos and a Next, or a
// walk that does not end, is an error.
func listAll(ctx context.Context, lister store.Lister, limit int) ([]record.Info, error) {
	var all []record.Info
	cursor := ""
	for range 1000 {
		page, err := lister.List(ctx, store.ListQuery{Limit: limit, Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list with limit %d and cursor %q: %w", limit, cursor, err)
		}
		if limit > 0 && len(page.Infos) > limit {
			return nil, failf("List with limit %d returned a page of %d Infos: a page holds at most Limit", limit, len(page.Infos))
		}
		all = append(all, page.Infos...)
		if page.Next == "" {
			return all, nil
		}
		if len(page.Infos) == 0 {
			return nil, failf("List with limit %d returned an empty page that names a Next: a page without Infos is the last", limit)
		}
		cursor = page.Next
	}
	return nil, failf("List with limit %d did not reach the last page in 1000 pages: Next must be empty after the last page", limit)
}

func idsOf(infos []record.Info) []string {
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.ID
	}
	return out
}
