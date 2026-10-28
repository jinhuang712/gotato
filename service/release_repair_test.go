package service_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// loadCountStore counts the Loads that reach the Store. A repair pass opens
// each marked Session from the store, so the count says whether a mark stood.
type loadCountStore struct {
	session.Store
	loads atomic.Int32
	hook  func()
}

func (s *loadCountStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	s.loads.Add(1)
	if s.hook != nil {
		s.hook()
	}
	return s.Store.Load(ctx, id)
}

// A Run that holds the Session lock may be the one that marked the Session
// (settle runs inside the lock). The repair pass finds the lock busy and must
// keep the mark, not forget the Session: the next pass looks at it again.
func TestRepairKeepsTheMarkOfASessionWhoseRunStillHoldsTheLock(t *testing.T) {
	store := &loadCountStore{Store: session.NewMemoryStore()}
	runner, id, hold, done := holdBeforeReleaseOn(t, store)
	runner.MarkUnreleased(id)
	runner.RepairReleases()
	close(hold.proceed)
	<-done
	// The Run released the Session itself. A kept mark makes this pass open
	// it from the store; a forgotten one leaves it alone.
	before := store.loads.Load()
	runner.RepairReleases()
	if store.loads.Load() == before {
		t.Fatal("the mark was forgotten while a Run held the Session")
	}
}

// A mark set while a repair attempt is under way is a new failure, and the
// attempt's success must not erase it.
func TestRepairDoesNotEraseAMarkSetDuringTheAttempt(t *testing.T) {
	store := &loadCountStore{Store: session.NewMemoryStore()}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
		Liveness: service.Liveness{TTL: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	rec, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := rec.ID()
	runner.MarkUnreleased(id)
	armed := true
	store.hook = func() {
		if armed {
			armed = false
			runner.MarkUnreleased(id)
		}
	}
	runner.RepairReleases()
	before := store.loads.Load()
	runner.RepairReleases()
	if store.loads.Load() == before {
		t.Fatal("a fresh mark was erased by the attempt that finished after it")
	}
}
