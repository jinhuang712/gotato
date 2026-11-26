package store

import (
	"context"
	"errors"
	"time"

	"github.com/jinhuang712/gotato/record"
)

// appendEntries appends entries to the Store after the position before the
// first of them, counts the write, and returns its failure: ErrConflict and
// record.ErrNotFound as they are, any other error as a store_failure. The
// caller holds w.mu.
func (w *Writer) appendEntries(ctx context.Context, entries []record.Entry, header record.Header) error {
	began := time.Now()
	err := w.store.Append(ctx, w.rec.ID(), entries[0].Seq-1, entries, header)
	w.count(entries, time.Since(began), err)
	if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, record.ErrNotFound) {
		err = storeFailure(err)
	}
	return err
}
