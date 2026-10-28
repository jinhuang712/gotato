package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// failOnceStore fails the first Append, once armed, that writes an Entry of the
// kind in failKind. The Store recovers at once, and beats counts the renewals
// of Liveness rows after arming, so that a test can wait for repair passes
// without sleeping.
type failOnceStore struct {
	session.Store
	failKind gotato.EventKind
	armed    atomic.Bool
	hit      atomic.Bool
	beats    atomic.Int32
	ticks    chan struct{}
}

func (s *failOnceStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	err := s.Store.Beat(ctx, row, ttl)
	if err == nil && ttl > 0 && s.armed.Load() && s.beats.Add(1) == 3 {
		close(s.ticks)
	}
	return err
}

func (s *failOnceStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, info session.RecordInfo) error {
	if s.armed.Load() {
		for _, e := range entries {
			if e.Kind == s.failKind && s.hit.CompareAndSwap(false, true) {
				return errors.New("injected transient store failure")
			}
		}
	}
	return s.Store.Append(ctx, id, after, entries, info)
}

// Every exit that holds a Claim must have the heartbeat give it back when a
// write of that exit failed, whatever the write was: a step of a Run, the
// release itself, a compaction, a notice, a tool's activation. The Instance
// stays healthy and renews its Liveness row, so nothing else would end the
// Claim, and another Instance would find the Session busy for good.
func TestFailedWriteOfAnyExitIsRepairedByTheHeartbeat(t *testing.T) {
	cases := []struct {
		name string
		kind gotato.EventKind
		// act runs the exit under test, after the fault is armed.
		act func(ctx context.Context, t *testing.T, r *service.Runner, id string)
	}{
		{"a step of a Run", gotato.EventKindReplyStarted, func(ctx context.Context, t *testing.T, r *service.Runner, id string) {
			if _, err := r.Run(ctx, service.RunRequest{SessionID: id, Prompt: "first"}); err == nil {
				t.Fatal("the Run did not see the fault")
			}
		}},
		{"the release of a Run that could not start", gotato.EventKindSessionReleased, func(ctx context.Context, t *testing.T, r *service.Runner, id string) {
			if _, err := r.Run(ctx, service.RunRequest{SessionID: id, Agent: "nope", Prompt: "x"}); !errors.Is(err, service.ErrUnknownAgent) {
				t.Fatalf("err = %v, want ErrUnknownAgent", err)
			}
		}},
		{"a compaction", gotato.EventKindCompactionStarted, func(ctx context.Context, t *testing.T, r *service.Runner, id string) {
			if _, err := r.Compact(ctx, id, modelctx.CompactConfig{KeepMessages: 1}); err == nil {
				t.Fatal("Compact did not see the fault")
			}
		}},
		{"a notice", gotato.EventKindNoticeQueued, func(ctx context.Context, t *testing.T, r *service.Runner, id string) {
			if _, err := r.Notice(ctx, service.NoticeRequest{SessionID: id, Kind: "k", Text: "t"}); err == nil {
				t.Fatal("Notice did not see the fault")
			}
		}},
		{"a tool's activation", gotato.EventKindMetadataChanged, func(ctx context.Context, t *testing.T, r *service.Runner, id string) {
			if _, err := r.SetToolActive(ctx, id, testkit.DemoToolID, false); err == nil {
				t.Fatal("SetToolActive did not see the fault")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			base := session.NewMemoryStore()
			faulty := &failOnceStore{Store: base, failKind: tc.kind, ticks: make(chan struct{})}
			agents := []service.Agent{{Name: "echo", Model: testkit.DemoModel{}, Tools: []gotato.Tool{testkit.DemoEchoTool()}, Warm: service.WarmPolicy{TTL: -1}}}
			liveness := service.Liveness{TTL: time.Second, Wait: 30 * time.Millisecond}
			a, err := service.New(service.Config{Store: faulty, Agents: agents, Liveness: liveness})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			b, err := service.New(service.Config{Store: base, Agents: agents, Liveness: liveness})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = b.Close(context.Background()) })

			rec, err := a.CreateSession(ctx, "echo", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Some history to compact.
			for _, prompt := range []string{"one", "two"} {
				if _, err := a.Run(ctx, service.RunRequest{SessionID: rec.ID(), Prompt: prompt}); err != nil {
					t.Fatal(err)
				}
			}
			faulty.armed.Store(true)
			tc.act(ctx, t, a, rec.ID())
			if !faulty.hit.Load() {
				t.Fatal("the fault was not exercised")
			}
			// The third renewal after the fault proves that two repair passes
			// have finished.
			select {
			case <-faulty.ticks:
			case <-ctx.Done():
				t.Fatal("the holder's heartbeat did not renew")
			}
			loaded, err := base.Load(ctx, rec.ID())
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Info.Busy || loaded.Info.Running != "" {
				t.Fatalf("the Session is still held after the store recovered: busy=%v running=%q", loaded.Info.Busy, loaded.Info.Running)
			}
			if _, err := b.Run(ctx, service.RunRequest{SessionID: rec.ID(), Prompt: "on the other instance"}); err != nil {
				t.Fatalf("the other Instance cannot run the Session: %v", err)
			}
		})
	}
}
