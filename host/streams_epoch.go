package host

import (
	"context"

	"github.com/jinhuang712/gotato/record"
)

// loadForStream loads the Record a client's stream opens on and checks that the
// client's position still means what it thinks: its epoch and number must be the
// Record's (ErrCannotResume).
func (r *Runner) loadForStream(ctx context.Context, sessionID string, from StreamFrom) (record.Loaded, error) {
	loaded, err := r.store.Records.Load(ctx, sessionID)
	if err != nil {
		return loaded, err
	}
	epoch := loaded.Header.Epoch
	if from.RecordEpoch != "" && from.RecordEpoch != epoch {
		return loaded, cannotResume("the Record is not the one the client saw", epoch, loaded.Seq)
	}
	if from.After > loaded.Seq {
		return loaded, cannotResume("the position is past the Record's last Event", epoch, loaded.Seq)
	}
	return loaded, nil
}

// epochVerifier returns the check of a stream bound to a Record's epoch.
// The epoch is checked after every read of the Record that returned Events,
// and before they are delivered: an epoch never comes back, so if the Record
// still has the stream's epoch after the read, the Events came from it.
func (r *Runner) epochVerifier(sessionID, epoch string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		now, err := r.store.Records.Load(ctx, sessionID)
		if err != nil {
			return err
		}
		if now.Header.Epoch != epoch {
			return cannotResume("the Record was created again", now.Header.Epoch, now.Seq)
		}
		return nil
	}
}

// startOpenedStream starts the stream, and closes sub and returns the error
// when the Record's epoch changed while the stream opened.
func (r *Runner) startOpenedStream(ctx context.Context, stream *SessionStream, sub *feedSubscription, loaded record.Loaded) error {
	epoch := loaded.Header.Epoch
	for {
		recheck, older, over := r.startStream(stream)
		if !recheck {
			for _, old := range older {
				old.failWith(cannotResume("the Record was created again", epoch, loaded.Seq))
			}
			if over {
				stream.endWith(NewReconnect("draining"))
			}
			return nil
		}
		if err := r.recheckEpoch(ctx, stream.sessionID, epoch); err != nil {
			_ = sub.Close()
			return err
		}
	}
}

// recheckEpoch reads the epoch of the Record again, and returns ErrCannotResume
// when it is not epoch.
func (r *Runner) recheckEpoch(ctx context.Context, sessionID, epoch string) error {
	// While this stream read the Record, this Instance saw the Record
	// with another epoch, or deleted it: read the epoch again, to know
	// which of the two is older.
	now, err := r.store.Records.Load(ctx, sessionID)
	if err == nil && now.Header.Epoch != epoch {
		err = cannotResume("the Record was created again", now.Header.Epoch, now.Seq)
	}
	return err
}
