package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// markDuringLoad marks the Session while a repair attempt reads it.
type markDuringLoad struct {
	session.Store
	armed chan struct{}
	hook  func()
}

func (s *markDuringLoad) Load(ctx context.Context, id string) (session.Loaded, error) {
	select {
	case <-s.armed:
		s.hook()
	default:
	}
	return s.Store.Load(ctx, id)
}

// A mark set while a repair attempt is under way is a new failure, and the
// attempt's success must not erase it.
func TestRepairDoesNotEraseAMarkSetDuringTheAttempt(t *testing.T) {
	base := session.NewMemoryStore()
	store := &markDuringLoad{Store: base, armed: make(chan struct{})}
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
	store.hook = func() { runner.MarkUnreleased(id) }
	runner.MarkUnreleased(id)
	close(store.armed)
	runner.RepairReleases()
	if !runner.Unreleased(id) {
		t.Fatal("a fresh mark was erased by the attempt that finished after it")
	}
}
