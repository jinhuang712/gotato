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

// A store that is out of reach for longer than the Liveness TTL expires the
// Instance's row, and the row never comes back. The Instance must take a new
// name and write again once the store answers, not fail every Run for good.
func TestInstanceRecoversWhenTheStoreReturnsAfterTheTTL(t *testing.T) {
	ctx := context.Background()
	store := &faultStore{Store: session.NewMemoryStore()}
	liveness := service.Liveness{TTL: 300 * time.Millisecond, Wait: time.Second}
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, liveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := a.Instance()

	store.fail("Beat", true)
	time.Sleep(2 * liveness.TTL)

	// While the store is out the Instance says so, and a Run is refused as
	// not run: it never reached the store.
	if !a.Lapsed() {
		t.Fatal("the Instance is not Lapsed after its row could not be renewed for two TTLs")
	}
	_, err = a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "during"})
	if !gotato.NotRun(err) {
		t.Fatalf("a Run refused because the row lapsed = %v, want not run", err)
	}

	store.fail("Beat", false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "after"})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Instance did not recover after the store returned: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if a.Lapsed() {
		t.Fatal("the Instance is still Lapsed after it wrote again")
	}
	if a.Instance() == before || !isInstance(a.Instance(), "instance-a") {
		t.Fatalf("Instance() = %q after recovery (was %q), want a new name with the same label", a.Instance(), before)
	}
	state, err := store.Instance(ctx, a.Instance())
	if err != nil || !state.Alive {
		t.Fatalf("the new name's row = %+v, %v; want alive", state, err)
	}
	// Its Claim is the new name's.
	record, err := a.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := record.Claim(); claim.Instance != a.Instance() {
		t.Fatalf("Claim = %+v, want the new name %q", claim, a.Instance())
	}
}
