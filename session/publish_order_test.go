package session_test

import (
	"context"
	"sync"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/session"
)

// Events reach the publisher in the order they were written, however many
// goroutines write: Flush publishes before it lets go of the lock that orders
// the writes.
func TestPublisherSeesEventsInWriteOrder(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	var mu sync.Mutex
	var numbers []uint64
	writer, err := session.Create(ctx, store, session.New(session.WithID("order")), "instance-a", session.WithPublisher(func(e gotato.Event) {
		mu.Lock()
		numbers = append(numbers, e.Head().Number)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	numbers = nil
	mu.Unlock()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				writer.Record().Set("k", string(rune('a'+g))+string(rune('a'+i%26)))
				if err := writer.Flush(ctx); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(numbers); i++ {
		if numbers[i] <= numbers[i-1] {
			t.Fatalf("published %d after %d", numbers[i], numbers[i-1])
		}
	}
	if len(numbers) == 0 {
		t.Fatal("nothing was published")
	}
}
