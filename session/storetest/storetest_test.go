package storetest_test

import (
	"context"
	"errors"
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

// mem is a MemoryStore with one rule of the Store contract broken. Each mutant
// below breaks exactly one rule written in the doc of session.Store; the
// conformance suite storetest.Run must fail every one of them.
type mem struct {
	*session.MemoryStore
	// hooks; nil means the real behavior
	append   func(m *mem, ctx context.Context, id string, after uint64, e []session.Entry, s session.RecordInfo) error
	create   func(m *mem, ctx context.Context, id string, e []session.Entry, s session.RecordInfo) error
	load     func(m *mem, ctx context.Context, id string) (session.Loaded, error)
	log      func(m *mem, ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error)
	del      func(m *mem, ctx context.Context, id string) error
	beat     func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error
	instance func(m *mem, ctx context.Context, id string) (session.InstanceState, error)
	list     func(m *mem, ctx context.Context) ([]session.RecordInfo, error)
}

func (m *mem) Append(ctx context.Context, id string, after uint64, e []session.Entry, s session.RecordInfo) error {
	if m.append != nil {
		return m.append(m, ctx, id, after, e, s)
	}
	return m.MemoryStore.Append(ctx, id, after, e, s)
}
func (m *mem) Create(ctx context.Context, id string, e []session.Entry, s session.RecordInfo) error {
	if m.create != nil {
		return m.create(m, ctx, id, e, s)
	}
	return m.MemoryStore.Create(ctx, id, e, s)
}
func (m *mem) Load(ctx context.Context, id string) (session.Loaded, error) {
	if m.load != nil {
		return m.load(m, ctx, id)
	}
	return m.MemoryStore.Load(ctx, id)
}
func (m *mem) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if m.log != nil {
		return m.log(m, ctx, id, after, limit)
	}
	return m.MemoryStore.Log(ctx, id, after, limit)
}
func (m *mem) Delete(ctx context.Context, id string) error {
	if m.del != nil {
		return m.del(m, ctx, id)
	}
	return m.MemoryStore.Delete(ctx, id)
}
func (m *mem) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if m.beat != nil {
		return m.beat(m, ctx, row, ttl)
	}
	return m.MemoryStore.Beat(ctx, row, ttl)
}
func (m *mem) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if m.instance != nil {
		return m.instance(m, ctx, id)
	}
	return m.MemoryStore.Instance(ctx, id)
}
func (m *mem) List(ctx context.Context) ([]session.RecordInfo, error) {
	if m.list != nil {
		return m.list(m, ctx)
	}
	return m.MemoryStore.List(ctx)
}

// survives reports whether storetest.Run passes on a store with the mutation.
func survives(mutate func(m *mem)) bool {
	tests := []testing.InternalTest{{Name: "suite", F: func(t *testing.T) {
		storetest.Run(t, func() (storetest.Open, func(), error) {
			shared := session.NewMemoryStore()
			return func() session.Store {
				m := &mem{MemoryStore: shared}
				mutate(m)
				return m
			}, nil, nil
		})
	}}}
	ok := testing.RunTests(func(string, string) (bool, error) { return true, nil }, tests)
	return ok
}

func TestTheSuiteCatchesEachBrokenStoreRule(t *testing.T) {
	mutants := map[string]func(m *mem){
		// Rule 3: an epoch that is not the stored Record's is ErrNotFound, "checked before the position".
		"position-checked-before-epoch": func(m *mem) {
			m.append = func(m *mem, ctx context.Context, id string, after uint64, e []session.Entry, s session.RecordInfo) error {
				if loaded, err := m.MemoryStore.Load(ctx, id); err == nil && loaded.Head != after {
					return session.ErrConflict
				}
				return m.MemoryStore.Append(ctx, id, after, e, s)
			}
		},
		// Rule 5/6: Beat renews the row, and replaces its Draining flag and Address.
		"beat-keeps-first-row": func(m *mem) {
			m.beat = func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
				if state, err := m.MemoryStore.Instance(ctx, row.ID); err == nil && state.Alive && ttl > 0 {
					row.Draining, row.Address = state.Draining, state.Address
				}
				return m.MemoryStore.Beat(ctx, row, ttl)
			}
		},
		"beat-does-not-renew": func(m *mem) {
			m.beat = func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
				if state, err := m.MemoryStore.Instance(ctx, row.ID); err == nil && state.Alive && ttl > 0 {
					return nil
				}
				return m.MemoryStore.Beat(ctx, row, ttl)
			}
		},
		// Rule 5: a Beat with a ttl of 0 on a missing row is not an error.
		"beat-zero-on-missing-row-fails": func(m *mem) {
			m.beat = func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
				if state, _ := m.MemoryStore.Instance(ctx, row.ID); ttl <= 0 && !state.Alive {
					if err := m.MemoryStore.Beat(ctx, row, 0); err != nil {
						return err
					}
					return errors.New("no such row")
				}
				return m.MemoryStore.Beat(ctx, row, ttl)
			}
		},
		// Create of one ID by two Instances at once: exactly one wins.
		"create-race-overwrites": func(m *mem) {
			m.create = func(m *mem, ctx context.Context, id string, e []session.Entry, s session.RecordInfo) error {
				if _, err := m.MemoryStore.Load(ctx, id); err == nil {
					return session.ErrExists
				}
				time.Sleep(20 * time.Millisecond)
				_ = m.MemoryStore.Delete(ctx, id)
				return m.MemoryStore.Create(ctx, id, e, s)
			}
		},
		// Log after the head is an empty answer.
		"log-past-head-is-an-error": func(m *mem) {
			m.log = func(m *mem, ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
				if loaded, err := m.MemoryStore.Load(ctx, id); err == nil && after > loaded.Head {
					return nil, session.ErrNotFound
				}
				return m.MemoryStore.Log(ctx, id, after, limit)
			}
		},
		// Delete of Record "a" must not touch Record "ab".
		"delete-removes-prefix-siblings": func(m *mem) {
			m.del = func(m *mem, ctx context.Context, id string) error {
				infos, _ := m.MemoryStore.List(ctx)
				err := m.MemoryStore.Delete(ctx, id)
				for _, info := range infos {
					if info.ID != id && len(info.ID) > len(id) && info.ID[:len(id)] == id {
						_ = m.MemoryStore.Delete(ctx, info.ID)
					}
				}
				return err
			}
		},
	}
	for name, mutate := range mutants {
		if survives(mutate) {
			t.Errorf("storetest.Run passes a store that breaks: %s", name)
		}
	}
}
