package service_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/session"
)

// watchedStore tells a test what a stream's catch-up has read, so the test
// goes on when the catch-up has, not after a delay. A stream reads its Record
// on a goroutine of its own: a Log read that returns Events (a batch), the
// Load that checks the Record's epoch, then a Log read that returns nothing,
// after which the stream is live.
type watchedStore struct {
	session.Store
	batches    atomic.Int64 // Log reads that returned Entries
	emptyReads atomic.Int64 // Log reads that returned none
	verifies   atomic.Int64 // Loads since the last batch
}

func (w *watchedStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	entries, err := w.Store.Log(ctx, id, after, limit)
	if err == nil {
		if len(entries) == 0 {
			w.emptyReads.Add(1)
		} else {
			w.verifies.Store(0)
			w.batches.Add(1)
		}
	}
	return entries, err
}

func (w *watchedStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	loaded, err := w.Store.Load(ctx, id)
	w.verifies.Add(1)
	return loaded, err
}

// waitLive waits until a catch-up that began after emptyReads was read as base
// has read its last, empty page.
func (w *watchedStore) waitLive(t *testing.T, base int64) {
	t.Helper()
	waitUntil(t, "the stream's catch-up to read its last, empty page", func() bool { return w.emptyReads.Load() > base })
}

// waitBatchRead waits until a catch-up that began after batches was read as
// base has read a batch and checked the Record's epoch, which leaves it
// holding the Events for the client.
func (w *watchedStore) waitBatchRead(t *testing.T, base int64) {
	t.Helper()
	waitUntil(t, "the stream's catch-up to read a batch and check the epoch", func() bool {
		return w.batches.Load() > base && w.verifies.Load() >= 1
	})
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
