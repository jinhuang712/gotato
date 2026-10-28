package service_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// lostAckStore applies an Append and then reports it failed, as a store does
// when the write lands and its answer is lost.
type lostAckStore struct {
	session.Store
	armed atomic.Bool
}

func (s *lostAckStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	err := s.Store.Append(ctx, id, after, entries, summary)
	if err == nil && s.armed.Load() {
		return errConnReset
	}
	return err
}

// A claim write that landed while the open failed is a Claim with no Writer:
// the repair pass must give it back, or the Session stays busy for good.
func TestAClaimThatLandedWithNoWriterIsGivenBackByTheRepair(t *testing.T) {
	ctx := context.Background()
	store := &lostAckStore{Store: session.NewMemoryStore()}
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	store.armed.Store(true)
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"}); err == nil {
		t.Fatal("the Run succeeded although its claim write reported a failure")
	}
	store.armed.Store(false)
	a.RepairReleases()
	loaded, err := store.Load(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Info.Busy || loaded.Info.Running != "" {
		t.Fatalf("the Session is still held after the repair: %+v", loaded.Info)
	}
}
