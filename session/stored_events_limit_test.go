package session_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/session"
)

// A limit bounds how many Entries StoredEvents reads, and a page never reads
// as the end of the log while Events remain after it.
func TestStoredEventsHonorsItsLimit(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	created, err := session.CreateWriter(ctx, store, session.New(), "instance-a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	id := created.Record().ID()
	all, err := session.StoredEvents(ctx, store, id, 0, 0)
	if err != nil || len(all) < 2 {
		t.Fatalf("all = %d Events, %v; want at least two", len(all), err)
	}
	var seen int
	var after uint64
	for {
		page, err := session.StoredEvents(ctx, store, id, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 1 {
			t.Fatalf("a page of %d Events with limit 1", len(page))
		}
		seen++
		after = page[len(page)-1].Head().Number
	}
	if seen != len(all) {
		t.Fatalf("paged %d Events, want %d", seen, len(all))
	}
}
