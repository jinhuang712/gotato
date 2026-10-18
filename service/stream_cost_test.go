package service_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// loadCounter counts Load calls.
type loadCounter struct {
	session.Store
	loads atomic.Int64
}

func (c *loadCounter) Load(ctx context.Context, id string) (session.Loaded, error) {
	c.loads.Add(1)
	return c.Store.Load(ctx, id)
}

// An idle stream costs one Log read per look, and no Load: the epoch is checked
// only after a read that returned Events.
func TestIdleStreamDoesNotLoadTheSessionOnEveryLook(t *testing.T) {
	store := &loadCounter{Store: session.NewMemoryStore()}
	a := recreatedRunner(t, "a", store, 10*time.Millisecond)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.StreamSession(ctx, first.SessionID, service.StreamFrom{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	readNumbers(t, stream, int(stream.Head))
	before := store.loads.Load()
	time.Sleep(300 * time.Millisecond) // about thirty looks
	if got := store.loads.Load() - before; got != 0 {
		t.Fatalf("an idle stream made %d Load calls in 300 ms, want none", got)
	}
}
