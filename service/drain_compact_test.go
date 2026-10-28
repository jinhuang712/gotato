package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// Drain "finishes every Run in flight, saved and released", but
// only Runs are counted (inflight). A Compact (and a Notice, a SetToolActive, a
// Delete) holds the Session's Claim without being admitted, so Drain returns
// nil while it is still going; Close then removes the Liveness row and the
// compaction's own write is refused. A deploy loses the compaction it was
// asked to wait for.
func TestDrainMustWaitForACompactionInFlight(t *testing.T) {
	store := newKindGate(session.NewMemoryStore(), gotato.EventKindCompactionEnded)
	hold := store.holds[gotato.EventKindCompactionEnded]
	runner := newRunnerOn(t, store, service.Agent{Name: "a", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"one", "two", "three"} {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: p}); err != nil {
			t.Fatal(err)
		}
	}
	compacted := make(chan error, 1)
	go func() {
		_, err := runner.Compact(ctx, s.ID(), modelctx.CompactConfig{KeepMessages: 1})
		compacted <- err
	}()
	waitChan(t, "the compaction's write", hold.entered)
	drained := make(chan error, 1)
	go func() { drained <- runner.Drain(ctx) }()
	select {
	case err := <-drained:
		loaded, loadErr := store.Load(ctx, s.ID())
		t.Errorf("Drain returned (%v) while a compaction still holds the Claim (busy=%v, load error %v)", err, loaded.Info.Busy, loadErr)
	case <-time.After(300 * time.Millisecond):
	}
	close(hold.proceed)
	if err := <-compacted; err != nil {
		t.Errorf("Compact = %v", err)
	}
}
