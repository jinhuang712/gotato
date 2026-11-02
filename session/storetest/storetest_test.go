package storetest_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/session/storetest"
)

// recordsOf is a Backing whose handles are wrapped by wrap, all on one
// MemoryStore.
func recordsOf(wrap func(*session.MemoryStore) session.Records) storetest.Backing {
	return func() (storetest.Open, func(), error) {
		shared := session.NewMemoryStore()
		return func() session.Records { return wrap(shared) }, nil, nil
	}
}

// livenessOf is a LivenessBacking whose handles are wrapped by wrap.
func livenessOf(wrap func(*session.MemoryStore) session.Liveness) storetest.LivenessBacking {
	return func() (storetest.OpenLiveness, func(), error) {
		shared := session.NewMemoryStore()
		return func() session.Liveness { return wrap(shared) }, nil, nil
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

// The reference stores pass every suite.
func TestTheReferenceStoresPass(t *testing.T) {
	ctx := context.Background()
	if problems := storetest.CheckRecords(ctx, recordsOf(func(m *session.MemoryStore) session.Records { return m })); len(problems) > 0 {
		t.Fatalf("MemoryStore records: %v", problems)
	}
	if problems := storetest.CheckLister(ctx, recordsOf(func(m *session.MemoryStore) session.Records { return m })); len(problems) > 0 {
		t.Fatalf("MemoryStore lister: %v", problems)
	}
	if problems := storetest.CheckLiveness(ctx, livenessOf(func(m *session.MemoryStore) session.Liveness { return m })); len(problems) > 0 {
		t.Fatalf("MemoryStore liveness: %v", problems)
	}
}

// rebasing moves a stale write onto the head instead of refusing it, so a
// writer that lost its Claim still writes.
type rebasing struct{ *session.MemoryStore }

func (s rebasing) Append(ctx context.Context, id string, _ uint64, entries []record.Entry, h record.Header) error {
	loaded, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	moved := make([]record.Entry, len(entries))
	for i, entry := range entries {
		entry.Seq = loaded.Seq + uint64(i) + 1
		moved[i] = entry
	}
	return s.MemoryStore.Append(ctx, id, loaded.Seq, moved, h)
}

func TestTheSuiteCatchesAStoreThatRebasesStaleWrites(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return rebasing{m} }))
	if !mentions(problems, "a stale writer is refused with ErrConflict and writes nothing") {
		t.Fatalf("problems = %v, want the stale writer check to fail", problems)
	}
}

// overwriting writes every Entry at its position, over whatever is there, so
// two copies can both write one position.
type overwriting struct {
	*session.MemoryStore
	mu      sync.Mutex
	records map[string][]record.Entry
}

func (s *overwriting) Create(ctx context.Context, id string, entries []record.Entry, h record.Header) error {
	if err := s.MemoryStore.Create(ctx, id, entries, h); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = append([]record.Entry(nil), entries...)
	return nil
}

func (s *overwriting) Append(ctx context.Context, id string, after uint64, entries []record.Entry, _ record.Header) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return record.ErrNotFound
	}
	rec = rec[:min(after, uint64(len(rec)))]
	s.records[id] = append(rec, entries...)
	return nil
}

func (s *overwriting) Log(ctx context.Context, id string, after uint64, limit int) ([]record.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return nil, record.ErrNotFound
	}
	if after >= uint64(len(rec)) {
		return nil, nil
	}
	rest := rec[after:]
	if limit > 0 && len(rest) > limit {
		rest = rest[:limit]
	}
	return append([]record.Entry(nil), rest...), nil
}

func TestTheSuiteCatchesAStoreThatOverwritesAPosition(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), func() (storetest.Open, func(), error) {
		store := &overwriting{MemoryStore: session.NewMemoryStore(), records: map[string][]record.Entry{}}
		return func() session.Records { return store }, nil, nil
	})
	if !mentions(problems, "a stale writer is refused with ErrConflict and writes nothing") || !mentions(problems, "of writers racing for one position, exactly one wins") {
		t.Fatalf("problems = %v, want the stale writer and race checks to fail", problems)
	}
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

func TestTheSuiteCatchesAStoreThatKeepsEndedRows(t *testing.T) {
	problems := storetest.CheckLiveness(context.Background(), func() (storetest.OpenLiveness, func(), error) {
		store := &keepingEndedRows{MemoryStore: session.NewMemoryStore(), ended: map[string]bool{}}
		return func() session.Liveness { return store }, nil, nil
	})
	if !mentions(problems, "a row ended with ttl 0 is removed, for every handle") {
		t.Fatalf("problems = %v, want the ended row check to fail", problems)
	}
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

func TestTheSuiteCatchesAStoreThatReadsARemovedRowAsAlive(t *testing.T) {
	problems := storetest.CheckLiveness(context.Background(), func() (storetest.OpenLiveness, func(), error) {
		store := &cachingInstances{MemoryStore: session.NewMemoryStore(), seen: map[string]session.InstanceState{}}
		return func() session.Liveness { return store }, nil, nil
	})
	if !mentions(problems, "Beat creates a Liveness row that Instance returns, and ttl 0 ends it") || !mentions(problems, "a row ended with ttl 0 is removed, for every handle") {
		t.Fatalf("problems = %v, want the liveness and ended row checks to fail", problems)
	}
}

// forgettingEpoch loses the epoch of the Header on its way out of Load.
type forgettingEpoch struct{ *session.MemoryStore }

func (s forgettingEpoch) Load(ctx context.Context, id string) (record.Loaded, error) {
	loaded, err := s.MemoryStore.Load(ctx, id)
	loaded.Header.Epoch = ""
	return loaded, err
}

func TestTheSuiteCatchesAStoreThatDropsTheEpoch(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return forgettingEpoch{m} }))
	if !mentions(problems, "Header.Epoch is kept by Create and Append and returned by Load") {
		t.Fatalf("problems = %v, want the epoch check to fail", problems)
	}
}

// columns maps a Header to the columns it knew, and drops the rest: LogFrom
// stands for a field added later, and State for bytes it does not understand.
type columns struct {
	*session.MemoryStore
	drop func(*record.Header)
}

func (s columns) Create(ctx context.Context, id string, entries []record.Entry, h record.Header) error {
	s.drop(&h)
	return s.MemoryStore.Create(ctx, id, entries, h)
}

func (s columns) Append(ctx context.Context, id string, after uint64, entries []record.Entry, h record.Header) error {
	s.drop(&h)
	return s.MemoryStore.Append(ctx, id, after, entries, h)
}

func TestTheSuiteCatchesAStoreThatDropsAField(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records {
		return columns{m, func(h *record.Header) { h.LogFrom = 0 }}
	}))
	if !mentions(problems, "every field of Header, RecordInfo and Entry is kept") {
		t.Fatalf("problems = %v, want the field check to fail", problems)
	}
}

func TestTheSuiteCatchesAStoreThatDropsTheState(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records {
		return columns{m, func(h *record.Header) { h.State = nil }}
	}))
	if !mentions(problems, "the Header's Epoch, Info and State are kept as written through Create, Append and Load") {
		t.Fatalf("problems = %v, want the header check to fail", problems)
	}
}

// lenientEpoch accepts a write that names no epoch, by naming one itself.
type lenientEpoch struct{ *session.MemoryStore }

func (s lenientEpoch) Create(ctx context.Context, id string, entries []record.Entry, h record.Header) error {
	if h.Epoch == "" {
		h.Epoch = "none"
	}
	return s.MemoryStore.Create(ctx, id, entries, h)
}

func TestTheSuiteCatchesAStoreThatAcceptsAnEmptyEpoch(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return lenientEpoch{m} }))
	if !mentions(problems, "Create and Append refuse a Header with no Epoch with invalid_argument") {
		t.Fatalf("problems = %v, want the empty epoch check to fail", problems)
	}
}

// lengthOnly compares only the length, as a store did before an Append named
// the epoch it wrote for: a writer of a deleted Record writes into the Record
// created again at the same length.
type lengthOnly struct{ *session.MemoryStore }

func (s lengthOnly) Append(ctx context.Context, id string, after uint64, entries []record.Entry, h record.Header) error {
	loaded, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	h.Epoch = loaded.Header.Epoch
	return s.MemoryStore.Append(ctx, id, after, entries, h)
}

func TestTheSuiteCatchesAStoreThatComparesOnlyTheLength(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return lengthOnly{m} }))
	if !mentions(problems, "Append by a writer of a deleted and re-created Record is ErrNotFound") {
		t.Fatalf("problems = %v, want the deleted writer check to fail", problems)
	}
}

// staleHeader answers a Load with the Entries it has now and the Header it
// returned the time before, as a store that reads the two in two steps, or
// caches the Header, does.
type staleHeader struct {
	*session.MemoryStore
	mu   sync.Mutex
	seen map[string]record.Header
}

func (s *staleHeader) Load(ctx context.Context, id string) (record.Loaded, error) {
	loaded, err := s.MemoryStore.Load(ctx, id)
	if err != nil {
		return loaded, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.seen[id]; ok {
		loaded.Header, old = old, loaded.Header
		s.seen[id] = old
		return loaded, nil
	}
	s.seen[id] = loaded.Header
	return loaded, nil
}

func TestTheSuiteCatchesALoadThatDoesNotReadInOneRead(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), func() (storetest.Open, func(), error) {
		store := &staleHeader{MemoryStore: session.NewMemoryStore(), seen: map[string]record.Header{}}
		return func() session.Records { return store }, nil, nil
	})
	if !mentions(problems, "a Load racing Appends reads the Record in one read") {
		t.Fatalf("problems = %v, want the racing load check to fail", problems)
	}
}

// ignoringLogFrom returns the facts only, whatever the Header asks for.
type ignoringLogFrom struct{ *session.MemoryStore }

func (s ignoringLogFrom) Load(ctx context.Context, id string) (record.Loaded, error) {
	loaded, err := s.MemoryStore.Load(ctx, id)
	facts := loaded.Entries[:0]
	for _, entry := range loaded.Entries {
		if entry.Layer == record.LayerFact {
			facts = append(facts, entry)
		}
	}
	loaded.Entries = facts
	return loaded, err
}

func TestTheSuiteCatchesALoadThatIgnoresLogFrom(t *testing.T) {
	problems := storetest.CheckRecords(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return ignoringLogFrom{m} }))
	if !mentions(problems, "Load returns every Entry from the Header's LogFrom, each once, and LogFrom is the last write's") {
		t.Fatalf("problems = %v, want the LogFrom check to fail", problems)
	}
}

// paging is a MemoryStore that lists with a wrong cursor: each Next is an
// offset into the listing, moved by shift from where the page ends.
type paging struct {
	*session.MemoryStore
	shift int
}

func (s paging) List(ctx context.Context, q session.ListQuery) (session.ListPage, error) {
	all, err := s.MemoryStore.List(ctx, session.ListQuery{Limit: 1000})
	if err != nil {
		return session.ListPage{}, err
	}
	start := 0
	if q.Cursor != "" {
		if start, err = strconv.Atoi(q.Cursor); err != nil {
			return session.ListPage{}, err
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := min(start+limit, len(all.Infos))
	page := session.ListPage{Infos: all.Infos[min(start, end):end]}
	if end < len(all.Infos) {
		next := end + s.shift
		if limit == 1 && s.shift < 0 {
			next = end
		}
		page.Next = strconv.Itoa(next)
	}
	return page, nil
}

func TestTheSuiteCatchesAListerThatRepeatsOrSkipsAcrossPages(t *testing.T) {
	for name, shift := range map[string]int{"repeats": -1, "skips": 1} {
		problems := storetest.CheckLister(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return paging{m, shift} }))
		if !mentions(problems, "List pages by Limit and Cursor, every Record once") {
			t.Errorf("a Lister that %s a Record across pages: problems = %v, want the paging check to fail", name, problems)
		}
	}
}

func TestTheListerSuiteNeedsAListerAndAcceptsAnyCursorFormat(t *testing.T) {
	problems := storetest.CheckLister(context.Background(), func() (storetest.Open, func(), error) {
		shared := session.NewMemoryStore()
		return func() session.Records { return onlyRecords{shared} }, nil, nil
	})
	if len(problems) == 0 || !strings.Contains(problems[0].Error(), "not a session.Lister") {
		t.Fatalf("problems = %v, want every check to say the Records is not a Lister", problems)
	}
	if problems := storetest.CheckLister(context.Background(), recordsOf(func(m *session.MemoryStore) session.Records { return paging{m, 0} })); len(problems) > 0 {
		t.Fatalf("a Lister with offsets as Cursors failed the suite: %v", problems)
	}
}

// onlyRecords hides everything of a MemoryStore but Records.
type onlyRecords struct{ session.Records }

// A fake clock lets the Liveness suite run with no sleep: the suite moves it
// through WithAdvance. A store that does not follow the clock it was told to
// advance fails the checks that need a row to expire.
func TestTheLivenessSuiteAdvancesAFakeClockInsteadOfSleeping(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	var advanced time.Duration
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
		advanced += d
	}
	began := time.Now()
	problems := storetest.CheckLiveness(context.Background(), func() (storetest.OpenLiveness, func(), error) {
		shared := session.NewMemoryStore(session.WithClock(clock))
		return func() session.Liveness { return shared }, nil, nil
	}, storetest.WithAdvance(advance))
	if len(problems) > 0 {
		t.Fatalf("problems = %v", problems)
	}
	if advanced == 0 {
		t.Fatal("the suite never advanced the clock")
	}
	if took := time.Since(began); took > 100*time.Millisecond {
		t.Fatalf("the suite took %v with an Advance, want it to sleep none", took)
	}

	problems = storetest.CheckLiveness(context.Background(), func() (storetest.OpenLiveness, func(), error) {
		shared := session.NewMemoryStore()
		return func() session.Liveness { return shared }, nil, nil
	}, storetest.WithAdvance(func(time.Duration) {}))
	if !mentions(problems, "an expired Liveness row is not alive, and Beat on it is ErrExpired") {
		t.Fatalf("problems = %v, want the expiry check to fail for a clock that was not advanced", problems)
	}
}

// mem is a MemoryStore with one rule of the store contract broken. Each mutant
// below breaks exactly one rule written in the doc of session.Records,
// session.Liveness or session.Lister; the matching suite must fail every one
// of them.
type mem struct {
	*session.MemoryStore
	// hooks; nil means the real behavior
	append   func(m *mem, ctx context.Context, id string, after uint64, e []record.Entry, h record.Header) error
	create   func(m *mem, ctx context.Context, id string, e []record.Entry, h record.Header) error
	load     func(m *mem, ctx context.Context, id string) (record.Loaded, error)
	log      func(m *mem, ctx context.Context, id string, after uint64, limit int) ([]record.Entry, error)
	del      func(m *mem, ctx context.Context, id string) error
	beat     func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error
	instance func(m *mem, ctx context.Context, id string) (session.InstanceState, error)
}

func (m *mem) Append(ctx context.Context, id string, after uint64, e []record.Entry, h record.Header) error {
	if m.append != nil {
		return m.append(m, ctx, id, after, e, h)
	}
	return m.MemoryStore.Append(ctx, id, after, e, h)
}
func (m *mem) Create(ctx context.Context, id string, e []record.Entry, h record.Header) error {
	if m.create != nil {
		return m.create(m, ctx, id, e, h)
	}
	return m.MemoryStore.Create(ctx, id, e, h)
}
func (m *mem) Load(ctx context.Context, id string) (record.Loaded, error) {
	if m.load != nil {
		return m.load(m, ctx, id)
	}
	return m.MemoryStore.Load(ctx, id)
}
func (m *mem) Log(ctx context.Context, id string, after uint64, limit int) ([]record.Entry, error) {
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

// survives reports whether the suite passes on a store with the mutation.
func survives(suite func(t *testing.T, shared *session.MemoryStore, mutate func(m *mem)), mutate func(m *mem)) bool {
	tests := []testing.InternalTest{{Name: "suite", F: func(t *testing.T) {
		suite(t, session.NewMemoryStore(), mutate)
	}}}
	return testing.RunTests(func(string, string) (bool, error) { return true, nil }, tests)
}

func records(t *testing.T, _ *session.MemoryStore, mutate func(m *mem)) {
	storetest.RunRecords(t, func() (storetest.Open, func(), error) {
		shared := session.NewMemoryStore()
		return func() session.Records {
			m := &mem{MemoryStore: shared}
			mutate(m)
			return m
		}, nil, nil
	})
}

func liveness(t *testing.T, _ *session.MemoryStore, mutate func(m *mem)) {
	storetest.RunLiveness(t, func() (storetest.OpenLiveness, func(), error) {
		shared := session.NewMemoryStore()
		return func() session.Liveness {
			m := &mem{MemoryStore: shared}
			mutate(m)
			return m
		}, nil, nil
	})
}

func TestTheSuiteCatchesEachBrokenStoreRule(t *testing.T) {
	type mutant struct {
		suite  func(t *testing.T, shared *session.MemoryStore, mutate func(m *mem))
		mutate func(m *mem)
	}
	mutants := map[string]mutant{
		// Rule 3: an epoch that is not the stored Record's is ErrNotFound, "checked before the position".
		"position-checked-before-epoch": {records, func(m *mem) {
			m.append = func(m *mem, ctx context.Context, id string, after uint64, e []record.Entry, h record.Header) error {
				if loaded, err := m.MemoryStore.Load(ctx, id); err == nil && loaded.Seq != after {
					return session.ErrConflict
				}
				return m.MemoryStore.Append(ctx, id, after, e, h)
			}
		}},
		// Liveness rule: Beat renews the row, and replaces its Draining flag and Address.
		"beat-keeps-first-row": {liveness, func(m *mem) {
			m.beat = func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
				if state, err := m.MemoryStore.Instance(ctx, row.ID); err == nil && state.Alive && ttl > 0 {
					row.Draining, row.Address = state.Draining, state.Address
				}
				return m.MemoryStore.Beat(ctx, row, ttl)
			}
		}},
		"beat-does-not-renew": {liveness, func(m *mem) {
			m.beat = func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
				if state, err := m.MemoryStore.Instance(ctx, row.ID); err == nil && state.Alive && ttl > 0 {
					return nil
				}
				return m.MemoryStore.Beat(ctx, row, ttl)
			}
		}},
		// Liveness rule: a Beat with a ttl of 0 on a missing row is not an error.
		"beat-zero-on-missing-row-fails": {liveness, func(m *mem) {
			m.beat = func(m *mem, ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
				if state, _ := m.MemoryStore.Instance(ctx, row.ID); ttl <= 0 && !state.Alive {
					if err := m.MemoryStore.Beat(ctx, row, 0); err != nil {
						return err
					}
					return errors.New("no such row")
				}
				return m.MemoryStore.Beat(ctx, row, ttl)
			}
		}},
		// Create of one ID by two Instances at once: exactly one wins.
		"create-race-overwrites": {records, func(m *mem) {
			m.create = func(m *mem, ctx context.Context, id string, e []record.Entry, h record.Header) error {
				if _, err := m.MemoryStore.Load(ctx, id); err == nil {
					return session.ErrExists
				}
				time.Sleep(20 * time.Millisecond)
				_ = m.MemoryStore.Delete(ctx, id)
				return m.MemoryStore.Create(ctx, id, e, h)
			}
		}},
		// Log after the head is an empty answer.
		"log-past-head-is-an-error": {records, func(m *mem) {
			m.log = func(m *mem, ctx context.Context, id string, after uint64, limit int) ([]record.Entry, error) {
				if loaded, err := m.MemoryStore.Load(ctx, id); err == nil && after > loaded.Seq {
					return nil, record.ErrNotFound
				}
				return m.MemoryStore.Log(ctx, id, after, limit)
			}
		}},
		// Delete of Record "a" must not touch Record "ab".
		"delete-removes-prefix-siblings": {records, func(m *mem) {
			m.del = func(m *mem, ctx context.Context, id string) error {
				page, _ := m.List(ctx, session.ListQuery{Limit: 1000})
				err := m.MemoryStore.Delete(ctx, id)
				for _, info := range page.Infos {
					if info.ID != id && len(info.ID) > len(id) && info.ID[:len(id)] == id {
						_ = m.MemoryStore.Delete(ctx, info.ID)
					}
				}
				return err
			}
		}},
		// Load must return the Header of the write its Entries are of.
		"load-returns-the-header-of-the-first-write": {records, func(m *mem) {
			m.load = func(m *mem, ctx context.Context, id string) (record.Loaded, error) {
				loaded, err := m.MemoryStore.Load(ctx, id)
				if err == nil && loaded.Seq > 1 {
					loaded.Header.Info.Messages = 1
				}
				return loaded, err
			}
		}},
	}
	for name, m := range mutants {
		if survives(m.suite, m.mutate) {
			t.Errorf("the suite passes a store that breaks: %s", name)
		}
	}
}
