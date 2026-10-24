package session

import (
	"context"
	"sync"
	"time"
)

// MemoryStore keeps Records and Liveness rows in memory. It is the store for
// library use and tests; nothing about the runtime requires anything more
// durable.
type MemoryStore struct {
	mu        sync.Mutex
	records   map[string]*memoryRecord
	instances map[string]memoryInstance
	now       func() time.Time
}

type memoryRecord struct {
	entries []Entry
	summary Summary
}

type memoryInstance struct {
	row     InstanceRow
	expires time.Time
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore(options ...StoreOption) *MemoryStore {
	config := newStoreConfig(options)
	return &MemoryStore{records: map[string]*memoryRecord{}, instances: map[string]memoryInstance{}, now: config.now}
}

// Create implements Store.
func (m *MemoryStore) Create(ctx context.Context, id string, entries []Entry, summary Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkBatch(0, entries); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[id]; ok {
		return ErrExists
	}
	m.records[id] = &memoryRecord{entries: cloneEntries(entries), summary: cloneSummary(summary)}
	return nil
}

// Append implements Store.
func (m *MemoryStore) Append(ctx context.Context, id string, after uint64, entries []Entry, summary Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkBatch(after, entries); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return ErrNotFound
	}
	if record.summary.Epoch() != summary.Epoch() {
		return ErrNotFound
	}
	if uint64(len(record.entries)) != after {
		return ErrConflict
	}
	record.entries = append(record.entries, cloneEntries(entries)...)
	record.summary = cloneSummary(summary)
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load(ctx context.Context, id string) (Loaded, error) {
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return Loaded{}, ErrNotFound
	}
	facts, segments := loadedFrom(record.entries)
	return Loaded{Facts: cloneEntries(facts), Segments: cloneEntries(segments), Head: uint64(len(record.entries)), Summary: cloneSummary(record.summary)}, nil
}

// Log implements Store.
func (m *MemoryStore) Log(ctx context.Context, id string, after uint64) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
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
	return cloneEntries(record.entries[after:]), nil
}

// List implements Store.
func (m *MemoryStore) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	out := make([]Summary, 0, len(m.records))
	for _, record := range m.records {
		out = append(out, cloneSummary(record.summary))
	}
	m.mu.Unlock()
	sortSummaries(out)
	return out, nil
}

// Delete implements Store.
func (m *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
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

// Beat implements Store.
func (m *MemoryStore) Beat(ctx context.Context, row InstanceRow, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
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

// Instance implements Store.
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
	return InstanceState{ID: id, Alive: m.now().Before(current.expires), Draining: current.row.Draining}, nil
}

func cloneSummary(summary Summary) Summary {
	out := summary
	if summary.Metadata != nil {
		out.Metadata = make(map[string]string, len(summary.Metadata))
		for k, v := range summary.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
