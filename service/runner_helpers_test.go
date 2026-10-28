package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func newRunner(t *testing.T, specs ...service.Agent) (*service.Runner, session.Store) {
	t.Helper()
	store := session.NewMemoryStore()
	if len(specs) == 0 {
		specs = []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}
	}
	runner, err := service.New(service.Config{Store: store, Agents: specs})
	if err != nil {
		t.Fatal(err)
	}
	return runner, store
}

// instanceRecorder records the Runner's Instance stream from now on.
func instanceRecorder(t *testing.T, runner *service.Runner, options ...gotato.SubscribeOption) *testkit.EventRecorder {
	t.Helper()
	sub, err := runner.SubscribeInstance(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	rec := testkit.RecordSubscription(sub)
	t.Cleanup(rec.Close)
	return rec
}

func kindsOf(events []gotato.Event) []gotato.EventKind {
	kinds := make([]gotato.EventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind()
	}
	return kinds
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
