package storetest_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/session/storetest"
)

// rebasing moves a stale write onto the head instead of refusing it, so a
// writer that lost its Claim still writes.
type rebasing struct{ *session.MemoryStore }

func (s rebasing) Append(ctx context.Context, id string, _ uint64, entries []session.Entry, summary session.RecordInfo) error {
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

func (s *overwriting) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if err := s.MemoryStore.Create(ctx, id, entries, summary); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = append([]session.Entry(nil), entries...)
	return nil
}

func (s *overwriting) Append(ctx context.Context, id string, after uint64, entries []session.Entry, _ session.RecordInfo) error {
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

func (s *overwriting) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, session.ErrNotFound
	}
	if after >= uint64(len(record)) {
		return nil, nil
	}
	rest := record[after:]
	if limit > 0 && len(rest) > limit {
		rest = rest[:limit]
	}
	return append([]session.Entry(nil), rest...), nil
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
	if !mentions(problems, "a stale writer is refused with ErrConflict and writes nothing") {
		t.Fatalf("problems = %v, want the stale writer check to fail", problems)
	}
}

func TestTheSuiteCatchesAStoreThatOverwritesAPosition(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := &overwriting{MemoryStore: session.NewMemoryStore(), records: map[string][]session.Entry{}}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "a stale writer is refused with ErrConflict and writes nothing") || !mentions(problems, "of writers racing for one position, exactly one wins") {
		t.Fatalf("problems = %v, want the stale writer and race checks to fail", problems)
	}
}

func TestTheSuiteCatchesAStoreThatKeepsEndedRows(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := &keepingEndedRows{MemoryStore: session.NewMemoryStore(), ended: map[string]bool{}}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "a row ended with ttl 0 is removed, for every handle") {
		t.Fatalf("problems = %v, want the ended row check to fail", problems)
	}
}

func TestTheSuiteCatchesAStoreThatReadsARemovedRowAsAlive(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := &cachingInstances{MemoryStore: session.NewMemoryStore(), seen: map[string]session.InstanceState{}}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "Beat creates a Liveness row that Instance returns, and ttl 0 ends it") || !mentions(problems, "a row ended with ttl 0 is removed, for every handle") {
		t.Fatalf("problems = %v, want the liveness and ended row checks to fail", problems)
	}
}

// forgettingEpoch maps the RecordInfo to its own columns and loses the epoch.
type forgettingEpoch struct{ *session.MemoryStore }

func (s forgettingEpoch) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	summary.RecordEpoch = ""
	return s.MemoryStore.Create(ctx, id, entries, summary)
}

func (s forgettingEpoch) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	summary.RecordEpoch = ""
	return s.MemoryStore.Append(ctx, id, after, entries, summary)
}

func TestTheSuiteCatchesAStoreThatDropsTheRecordEpoch(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := forgettingEpoch{session.NewMemoryStore()}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "RecordInfo.RecordEpoch is kept by Create and Append and returned by Load and List") {
		t.Fatalf("problems = %v, want the record epoch check to fail", problems)
	}
}

// columns maps a RecordInfo to the columns it knew, and drops the rest: the
// last run end stands for a field added later.
type columns struct{ *session.MemoryStore }

func (s columns) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	summary.LastRunEnd = time.Time{}
	return s.MemoryStore.Create(ctx, id, entries, summary)
}

func (s columns) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	summary.LastRunEnd = time.Time{}
	return s.MemoryStore.Append(ctx, id, after, entries, summary)
}

func TestTheSuiteCatchesAStoreThatDropsAField(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := columns{session.NewMemoryStore()}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "every field of RecordInfo, Entry and InstanceRow is kept") {
		t.Fatalf("problems = %v, want the field check to fail", problems)
	}
}

// lenientEpoch accepts a write that names no epoch, by naming one itself.
type lenientEpoch struct{ *session.MemoryStore }

func (s lenientEpoch) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if summary.RecordEpoch == "" {
		summary.RecordEpoch = "none"
	}
	return s.MemoryStore.Create(ctx, id, entries, summary)
}

func TestTheSuiteCatchesAStoreThatAcceptsAnEmptyEpoch(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := lenientEpoch{session.NewMemoryStore()}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "Create and Append refuse a RecordInfo with no RecordEpoch with invalid_argument") {
		t.Fatalf("problems = %v, want the empty epoch check to fail", problems)
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

// lengthOnly compares only the length, as a store did before an Append named
// the epoch it wrote for: a writer of a deleted Record writes into the Record
// created again at the same length.
type lengthOnly struct{ *session.MemoryStore }

func (s lengthOnly) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	loaded, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	summary.RecordEpoch = loaded.Info.RecordEpoch
	return s.MemoryStore.Append(ctx, id, after, entries, summary)
}

func TestTheSuiteCatchesAStoreThatComparesOnlyTheLength(t *testing.T) {
	problems := storetest.Check(context.Background(), func() (storetest.Open, func(), error) {
		store := lengthOnly{session.NewMemoryStore()}
		return func() session.Store { return store }, nil, nil
	})
	if !mentions(problems, "Append by a writer of a deleted and re-created Record is ErrNotFound") {
		t.Fatalf("problems = %v, want the deleted writer check to fail", problems)
	}
}
