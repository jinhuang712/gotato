package storetest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/session/storetest"
)

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
