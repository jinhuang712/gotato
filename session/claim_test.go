package session_test

import (
	"context"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/session"
)

// A Checkpoint keeps the Claim: a load that starts at a compaction_ended still
// names the Writer and its Epoch, so another Instance's holder check sees a
// live holder, and a later Claim continues the Epoch.
func TestCheckpointKeepsTheClaim(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	r := session.New(session.WithMessages(gotato.UserMessage("a"), gotato.UserMessage("b")))
	if _, err := session.Create(ctx, store, r, "instance-a", session.WithIdle()); err != nil {
		t.Fatal(err)
	}
	wa, err := session.OpenWriter(ctx, store, r.ID(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	wa.Record().Checkpoint(gotato.CompactionPastCeiling, []gotato.Message{gotato.UserMessage("summary")}, nil)
	if err := wa.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var seen session.Claim
	var seenBusy bool
	wb, err := session.OpenWriter(ctx, store, r.ID(), "instance-b", session.WithHolderCheck(func(_ context.Context, holder session.Claim, busy bool) error {
		seen, seenBusy = holder, busy
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if seen != (session.Claim{Instance: "instance-a", Epoch: 1}) || !seenBusy {
		t.Fatalf("the holder check saw %+v busy=%v, want instance-a at epoch 1, busy", seen, seenBusy)
	}
	if claim := wb.Record().Claim(); claim != (session.Claim{Instance: "instance-b", Epoch: 2}) {
		t.Fatalf("claim = %+v, want instance-b at epoch 2", claim)
	}
	events, err := session.StoredEvents(ctx, store, r.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var claimed gotato.SessionClaimed
	for _, event := range events {
		if c, ok := event.(gotato.SessionClaimed); ok {
			claimed = c
		}
	}
	if claimed.Instance != "instance-b" || claimed.From != "instance-a" {
		t.Fatalf("last claim = %+v, want instance-b from instance-a", claimed)
	}
}
