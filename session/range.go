package session

import (
	"context"
	"errors"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// EntryLog is the part of a Store that reads a Record's log. Every Store is
// one.
type EntryLog interface {
	Log(ctx context.Context, id string, after uint64) ([]Entry, error)
}

// CommittedMessages reads back the Messages that Entries from to to (both
// included) of Record id committed, in order, as they were: this is how a
// Segment's originals are read once the Context holds only its rendering. A
// Message the log holds twice is returned once.
//
// complete is false when the range cannot be read in full: from is 0 (the
// Segment did not record where its Messages are), the Record is gone, or the
// Entries at the start of the range were removed, which a team does when it
// prunes the log before a Checkpoint. What was left is still returned.
func CommittedMessages(ctx context.Context, log EntryLog, id string, from, to uint64) (messages []gotato.Message, complete bool, err error) {
	if from == 0 || to < from {
		return nil, false, nil
	}
	entries, err := log.Log(ctx, id, from-1)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	complete = len(entries) > 0 && entries[0].Seq == from
	seen := map[gotato.MessageID]bool{}
	for _, entry := range entries {
		if entry.Seq > to {
			break
		}
		event, ok, err := EventOf(entry, gotato.SessionID(id))
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		message, ok := messageOf(event)
		if !ok || (message.ID != "" && seen[message.ID]) {
			continue
		}
		seen[message.ID] = true
		messages = append(messages, message)
	}
	return messages, complete, nil
}
