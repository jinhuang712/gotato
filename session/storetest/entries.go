package storetest

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

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

func segment(seq uint64, writer string) session.Entry {
	return entry(seq, gotato.EventKindSegmentClosed, session.LayerFact, writer)
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

// create writes a Record with a checkpoint, a fact and a log Entry.
func create(ctx context.Context, store session.Store, id string) ([]session.Entry, error) {
	entries := []session.Entry{checkpoint(1, "a"), fact(2, "a"), logEntry(3, "a")}
	entries[1].Instance = "instance-a"
	return entries, store.Create(ctx, id, entries, summary(id, 3, "2026-10-17T00:00:03.000Z"))
}
