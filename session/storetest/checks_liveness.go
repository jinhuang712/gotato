package storetest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/session"
)

func checkLiveness(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-a", Draining: true, Address: "http://a.example:8080"}, time.Hour); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	state, err := open().Instance(ctx, "instance-a")
	if err != nil || !state.Alive || !state.Draining {
		return failf("Instance after Beat returned %+v, %v, want alive and draining: Beat must store the whole InstanceRow", state, err)
	}
	if state.Address != "http://a.example:8080" {
		return failf("Instance returned Address %q, want the one its row advertised: Beat must store InstanceRow.Address", state.Address)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-a"}, 0); err != nil {
		return failf("Beat with ttl 0: %w", err)
	}
	if state, err := open().Instance(ctx, "instance-a"); err != nil || state.Alive {
		return failf("Instance after Beat with ttl 0 returned %+v, %v, want not alive: ttl 0 ends the row", state, err)
	}
	if state, err := open().Instance(ctx, "never-seen"); err != nil || state.Alive {
		return failf("Instance of an ID never seen returned %+v, %v, want not alive and no error", state, err)
	}
	return nil
}

func checkExpiry(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-b"}, 50*time.Millisecond); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	time.Sleep(120 * time.Millisecond)
	if state, err := open().Instance(ctx, "instance-b"); err != nil || state.Alive {
		return failf("Instance after its ttl returned %+v, %v, want not alive: a row expires by the store's clock", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-b"}, time.Hour); !errors.Is(err, session.ErrExpired) {
		return failf("Beat after the row's ttl returned %v, want ErrExpired: an expired row must stay, so that it never comes back", err)
	}
	return nil
}

// checkEndedRemoved checks that a row its Instance ended, with a ttl of 0, is
// removed: from then on the Instance is not alive for any handle, so every
// Claim that names it is dead, and nothing of the row is left, so its ID is
// unknown again.
func checkEndedRemoved(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c", Draining: true}, time.Hour); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	reader := open()
	if state, err := reader.Instance(ctx, "instance-c"); err != nil || !state.Alive {
		return failf("Instance after Beat returned %+v, %v, want alive", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c"}, 0); err != nil {
		return failf("Beat with ttl 0: %w", err)
	}
	if state, err := reader.Instance(ctx, "instance-c"); err != nil || state.Alive || state.Draining {
		return failf("Instance after Beat with ttl 0 returned %+v, %v, want not alive, from every handle: ttl 0 removes the row", state, err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-c"}, 0); err != nil {
		return failf("Beat with ttl 0 on a removed row returned %v, want nil", err)
	}
	if err := open().Beat(ctx, session.InstanceRow{ID: "instance-c"}, time.Hour); err != nil {
		return failf("Beat after the row was removed returned %v, want a new row: ttl 0 must leave nothing of the old row, so no ErrExpired", err)
	}
	return nil
}

// checkBeatRenews checks that a Beat on a live row renews it, and replaces the
// whole row: the Draining flag and the Address are the last Beat's.
func checkBeatRenews(ctx context.Context, open Open) error {
	store := open()
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-d", Address: "http://old.example:1"}, 80*time.Millisecond); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	if err := store.Beat(ctx, session.InstanceRow{ID: "instance-d", Draining: true, Address: "http://new.example:2"}, time.Hour); err != nil {
		return failf("a second Beat on a live row returned %v, want nil: Beat renews the row", err)
	}
	time.Sleep(160 * time.Millisecond)
	state, err := open().Instance(ctx, "instance-d")
	if err != nil || !state.Alive {
		return failf("Instance after a Beat that renewed the row for an hour returned %+v, %v, want alive: Beat renews the row until ttl from now, not from the first Beat", state, err)
	}
	if !state.Draining || state.Address != "http://new.example:2" {
		return failf("Instance after a second Beat returned %+v, want Draining and the second Address: Beat replaces the whole row", state)
	}
	return nil
}

// checkBeatEndsMissingRow checks that a Beat with a ttl of 0 on a row that is
// not there is not an error.
func checkBeatEndsMissingRow(ctx context.Context, open Open) error {
	if err := open().Beat(ctx, session.InstanceRow{ID: "instance-never"}, 0); err != nil {
		return failf("Beat with ttl 0 on an Instance with no row returned %v, want nil: ending a row twice, or one never written, is not an error", err)
	}
	if state, err := open().Instance(ctx, "instance-never"); err != nil || state.Alive {
		return failf("Instance after Beat with ttl 0 on no row returned %+v, %v, want not alive", state, err)
	}
	return nil
}
