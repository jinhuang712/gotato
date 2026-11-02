package session

import (
	"context"
	"sync"
	"time"
)

// MemoryStore keeps Records and Liveness rows in memory. It is the store for
// library use and tests; nothing about the runtime requires anything more
// durable. It is Records, Liveness and Lister, and a Store takes it for
// each: Store{Records: m, Liveness: m}.
type MemoryStore struct {
	mu        sync.Mutex
	records   map[string]*memoryRecord
	instances map[string]memoryInstance
	now       func() time.Time
}

type memoryRecord struct {
	entries []Entry
	header  Header
}

type memoryInstance struct {
	row     InstanceRow
	expires time.Time
}

// StoreOption configures NewMemoryStore and NewFileStore.
type StoreOption func(*storeConfig)

type storeConfig struct {
	now func() time.Time
}

// WithClock sets the clock that times Liveness rows, in place of the
// machine's. A test that moves a clock by hand moves the time a row expires by,
// so it never sleeps to see one expire.
func WithClock(now func() time.Time) StoreOption {
	return func(c *storeConfig) { c.now = now }
}

func newStoreConfig(options []StoreOption) storeConfig {
	config := storeConfig{now: time.Now}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.now == nil {
		config.now = time.Now
	}
	return config
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore(options ...StoreOption) *MemoryStore {
	config := newStoreConfig(options)
	return &MemoryStore{records: map[string]*memoryRecord{}, instances: map[string]memoryInstance{}, now: config.now}
}

// Create implements Records.
func (m *MemoryStore) Create(ctx context.Context, id string, entries []Entry, h Header) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckID(id); err != nil {
		return err
	}
	if err := CheckBatch(0, entries, h); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[id]; ok {
		return ErrExists
	}
	m.records[id] = &memoryRecord{entries: cloneEntries(entries), header: cloneHeader(h)}
	return nil
}

// Append implements Records.
func (m *MemoryStore) Append(ctx context.Context, id string, after uint64, entries []Entry, h Header) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckID(id); err != nil {
		return err
	}
	if err := CheckBatch(after, entries, h); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return ErrNotFound
	}
	if record.header.Epoch != h.Epoch {
		return ErrNotFound
	}
	if uint64(len(record.entries)) != after {
		return ErrConflict
	}
	record.entries = append(record.entries, cloneEntries(entries)...)
	record.header = cloneHeader(h)
	return nil
}

// Load implements Records.
func (m *MemoryStore) Load(ctx context.Context, id string) (Loaded, error) {
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	if err := CheckID(id); err != nil {
		return Loaded{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return Loaded{}, ErrNotFound
	}
	entries, segments := loadedFrom(record.entries, record.header.LogFrom)
	return Loaded{Entries: cloneEntries(entries), Segments: cloneEntries(segments), Seq: uint64(len(record.entries)), Header: cloneHeader(record.header)}, nil
}

// Log implements Records.
func (m *MemoryStore) Log(ctx context.Context, id string, after uint64, limit int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := CheckID(id); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return nil, ErrNotFound
	}
	if after >= uint64(len(record.entries)) {
		return nil, nil
	}
	rest := record.entries[after:]
	if limit > 0 && len(rest) > limit {
		rest = rest[:limit]
	}
	return cloneEntries(rest), nil
}

// List implements Lister: a page of the Records, the most recently updated
// first, 100 when the query names no Limit.
func (m *MemoryStore) List(ctx context.Context, q ListQuery) (ListPage, error) {
	if err := ctx.Err(); err != nil {
		return ListPage{}, err
	}
	m.mu.Lock()
	infos := make([]RecordInfo, 0, len(m.records))
	for _, record := range m.records {
		infos = append(infos, cloneInfo(record.header.Info))
	}
	m.mu.Unlock()
	return pageOf(infos, q)
}

// Delete implements Records.
func (m *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckID(id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[id]; !ok {
		return ErrNotFound
	}
	delete(m.records, id)
	return nil
}

// Beat implements Liveness.
func (m *MemoryStore) Beat(ctx context.Context, row InstanceRow, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CheckID(row.ID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ttl <= 0 {
		delete(m.instances, row.ID)
		return nil
	}
	now := m.now()
	if current, ok := m.instances[row.ID]; ok && !now.Before(current.expires) {
		return ErrExpired
	}
	m.instances[row.ID] = memoryInstance{row: row, expires: now.Add(ttl)}
	return nil
}

// Instance implements Liveness.
func (m *MemoryStore) Instance(ctx context.Context, id string) (InstanceState, error) {
	if err := ctx.Err(); err != nil {
		return InstanceState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.instances[id]
	if !ok {
		return InstanceState{ID: id}, nil
	}
	return InstanceState{ID: id, Alive: m.now().Before(current.expires), Draining: current.row.Draining, Address: current.row.Address}, nil
}

var (
	_ Records  = (*MemoryStore)(nil)
	_ Lister   = (*MemoryStore)(nil)
	_ Liveness = (*MemoryStore)(nil)
)
