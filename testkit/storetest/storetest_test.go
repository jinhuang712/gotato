package storetest_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit/storetest"
)

// rebasing moves a stale write onto the head instead of refusing it, so a
// writer that lost its Claim still writes.
type rebasing struct{ *session.MemoryStore }

func (s rebasing) Append(ctx context.Context, id string, _ uint64, entries []session.Entry, summary session.Summary) error {
	loaded, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	moved := make([]session.Entry, len(entries))
	for i, entry := range entries {
		entry.Seq = loaded.Head + uint64(i) + 1
		moved[i] = entry
	}
	return s.MemoryStore.Append(ctx, id, loaded.Head, moved, summary)
}

// overwriting writes every Entry at its position, over whatever is there, so
// two copies can both write one position.
type overwriting struct {
	*session.MemoryStore
	mu      sync.Mutex
	records map[string][]session.Entry
}

func (s *overwriting) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	if err := s.MemoryStore.Create(ctx, id, entries, summary); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = append([]session.Entry(nil), entries...)
	return nil
}

func (s *overwriting) Append(ctx context.Context, id string, after uint64, entries []session.Entry, _ session.Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return session.ErrNotFound
	}
	record = record[:min(after, uint64(len(record)))]
	s.records[id] = append(record, entries...)
	return nil
}

func (s *overwriting) Log(ctx context.Context, id string, after uint64) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, session.ErrNotFound
	}
	if after >= uint64(len(record)) {
		return nil, nil
	}
	return append([]session.Entry(nil), record[after:]...), nil
}

func TestTheSuiteCatchesAStoreThatRebasesStaleWrites(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := rebasing{session.NewMemoryStore()}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "a stale writer is refused") {
		t.Fatalf("problems = %v, want the stale writer check to fail", problems)
	}
}

func TestTheSuiteCatchesAStoreThatOverwritesAPosition(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := &overwriting{MemoryStore: session.NewMemoryStore(), records: map[string][]session.Entry{}}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "a stale writer is refused") || !mentions(problems, "one writer wins a position") {
		t.Fatalf("problems = %v, want the stale writer and race checks to fail", problems)
	}
}

func mentions(problems []error, check string) bool {
	for _, problem := range problems {
		if strings.HasPrefix(problem.Error(), check+":") {
			return true
		}
	}
	return false
}
