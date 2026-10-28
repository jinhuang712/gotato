package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// releaseFailStore fails the write that releases a Session while failRelease
// is set, as a store blip at the wrong moment would.
type releaseFailStore struct {
	session.Store
	failRelease atomic.Bool
	failed      atomic.Int64
}

func (s *releaseFailStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	if s.failRelease.Load() {
		for _, entry := range entries {
			if entry.Kind == gotato.EventKindSessionReleased {
				s.failed.Add(1)
				return errors.New("store blip")
			}
		}
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

// The write that releases a Session fails. The Run keeps its status, but the
// Session stays marked busy under an Instance that is alive and idle, so no
// other Instance may take it. The Instance repairs the release itself once
// the store answers, without a request for the Session.
func TestFailedReleaseIsRepairedByTheHolder(t *testing.T) {
	ctx := context.Background()
	backing := session.NewMemoryStore()
	flaky := &releaseFailStore{Store: backing}
	liveness := service.Liveness{TTL: 600 * time.Millisecond, Wait: 300 * time.Millisecond}
	a := instanceRunner(t, flaky, "instance-a", testkit.EchoModel{}, liveness)
	b := instanceRunner(t, backing, "instance-b", testkit.EchoModel{}, liveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}

	flaky.failRelease.Store(true)
	response, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "one"})
	if err != nil || response.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Run = %q, %v; a failed release keeps the Run completed", response.Result.Status, err)
	}
	if flaky.failed.Load() == 0 {
		t.Fatal("the release write did not fail: the test proves nothing")
	}
	// The store answers again; nothing sends A a request for the Session.
	flaky.failRelease.Store(false)

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err = b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "two"})
		if err == nil {
			return
		}
		if !gotato.IsCode(err, gotato.CodeBusy) || time.Now().After(deadline) {
			t.Fatalf("another Instance's Run on the Session = %v; it stays busy under an idle holder", err)
		}
	}
}
