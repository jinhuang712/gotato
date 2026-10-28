package service_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/session"
)

// errConnReset is the raw error a faulty store returns, as a driver would.
var errConnReset = errors.New("connection reset")

// faultStore fails the store calls a test names with a raw error, and passes
// the others through. A test turns a call on and off while Runs go on.
type faultStore struct {
	session.Store
	mu   sync.Mutex
	down map[string]bool
}

// fail makes the call named op ("Load", "Create", "Append", "Beat", "Delete",
// "Log", "Instance") fail while on is true.
func (s *faultStore) fail(op string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down == nil {
		s.down = map[string]bool{}
	}
	s.down[op] = on
}

func (s *faultStore) failing(op string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down[op]
}

func (s *faultStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	if s.failing("Load") {
		return session.Loaded{}, errConnReset
	}
	return s.Store.Load(ctx, id)
}

func (s *faultStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.failing("Create") {
		return errConnReset
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *faultStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	if s.failing("Append") {
		return errConnReset
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func (s *faultStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.failing("Beat") {
		return errConnReset
	}
	return s.Store.Beat(ctx, row, ttl)
}

func (s *faultStore) Delete(ctx context.Context, id string) error {
	if s.failing("Delete") {
		return errConnReset
	}
	return s.Store.Delete(ctx, id)
}

func (s *faultStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if s.failing("Log") {
		return nil, errConnReset
	}
	return s.Store.Log(ctx, id, after, limit)
}

func (s *faultStore) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if s.failing("Instance") {
		return session.InstanceState{}, errConnReset
	}
	return s.Store.Instance(ctx, id)
}
