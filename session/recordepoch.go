package session

import (
	"context"
	"errors"

	"github.com/jinhuang712/gotato/gotato"
)

// SameRecord reports whether the Record stored under id is the one with this
// epoch (Record.RecordEpoch). A reference from one Record to another's log
// (SpilledResult, SegmentSource) keeps the epoch, so that a Record deleted and
// created again under the same ID is not read as the one the reference meant.
// It is false when there is no Record under id. An empty epoch is a reference
// that has none, and any Record under id is then the same. A log that is not a
// Store is asked for the Record's first Entry, and when that was removed, or
// holds no epoch, the answer is false.
func SameRecord(ctx context.Context, log EntryLog, id, epoch string) (bool, error) {
	if epoch == "" {
		return true, nil
	}
	if store, ok := log.(interface {
		Load(ctx context.Context, id string) (Loaded, error)
	}); ok {
		loaded, err := store.Load(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return err == nil && loaded.Info.RecordEpoch == epoch, err
	}
	// [CONTRACT] an EntryLog that is not a Store has no Load; the exported seam is asked for the first Entry.
	entries, err := log.Log(ctx, id, 0, 1)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil || len(entries) == 0 || entries[0].Seq != 1 || entries[0].Kind != gotato.EventKindSessionCreated {
		return false, err
	}
	event, ok, err := eventOf(entries[0], gotato.SessionID(id))
	if created, isCreated := event.(gotato.SessionCreated); ok && isCreated {
		return created.RecordEpoch == epoch, err
	}
	return false, err
}
