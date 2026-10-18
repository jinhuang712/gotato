package storetest_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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

// keepingEndedRows keeps a row that its Instance ended, expired, so it can
// never be renewed: ended rows pile up, one for every Instance that stopped.
type keepingEndedRows struct {
	*session.MemoryStore
	mu    sync.Mutex
	ended map[string]bool
}

func (s *keepingEndedRows) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended[row.ID] {
		return session.ErrExpired
	}
	if ttl <= 0 {
		s.ended[row.ID] = true
	}
	return s.MemoryStore.Beat(ctx, row, ttl)
}

// cachingInstances answers Instance from what it last saw of each row and
// never drops what it saw, so a row that was removed still reads as alive,
// and a Claim that names a stopped Instance looks valid.
type cachingInstances struct {
	*session.MemoryStore
	mu   sync.Mutex
	seen map[string]session.InstanceState
}

func (s *cachingInstances) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	state, err := s.MemoryStore.Instance(ctx, id)
	if err != nil {
		return state, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.Alive {
		s.seen[id] = state
		return state, nil
	}
	if cached, ok := s.seen[id]; ok {
		return cached, nil
	}
	return state, nil
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

func TestTheSuiteCatchesAStoreThatKeepsEndedRows(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := &keepingEndedRows{MemoryStore: session.NewMemoryStore(), ended: map[string]bool{}}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "an ended row is removed") {
		t.Fatalf("problems = %v, want the ended row check to fail", problems)
	}
}

func TestTheSuiteCatchesAStoreThatReadsARemovedRowAsAlive(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := &cachingInstances{MemoryStore: session.NewMemoryStore(), seen: map[string]session.InstanceState{}}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "liveness rows") || !mentions(problems, "an ended row is removed") {
		t.Fatalf("problems = %v, want the liveness and ended row checks to fail", problems)
	}
}

// forgettingEpoch maps the Summary to its own columns and loses the epoch.
type forgettingEpoch struct{ *session.MemoryStore }

func (s forgettingEpoch) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	summary.RecordEpoch = ""
	return s.MemoryStore.Create(ctx, id, entries, summary)
}

func (s forgettingEpoch) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	summary.RecordEpoch = ""
	return s.MemoryStore.Append(ctx, id, after, entries, summary)
}

func TestTheSuiteCatchesAStoreThatDropsTheRecordEpoch(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := forgettingEpoch{session.NewMemoryStore()}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "the record epoch survives every read") {
		t.Fatalf("problems = %v, want the record epoch check to fail", problems)
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
