package service

import (
	"context"
	"errors"
	"math"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// notWriter answers a message for a Run that another Instance writes. It was
// not run, and a retry at the writer may succeed. The address is the writer's
// advertised one, absent when it advertises none.
func notWriter(instance, address string) *gotato.RuntimeError {
	e := gotato.ErrorOf(gotato.CodeNotWriter, "service: the session's run is going on another instance")
	e.Details = map[string]string{"writer_instance": instance}
	if address != "" {
		e.Details["writer"] = address
		e.Message += " (" + address + ")"
	}
	return e
}

// WriterOf returns the advertised address in a not_writer error, and whether
// err is one. The address is empty when the writer advertises none.
func WriterOf(err error) (address string, ok bool) {
	var runtimeErr *gotato.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != gotato.CodeNotWriter {
		return "", false
	}
	return runtimeErr.Details["writer"], true
}

// reachStore makes one cheap read of the Session's Record with the request's
// ctx, so that a call which acts on a Run in memory goes through the store
// like every other request. A store wrapper that refuses the caller answers
// here, and the error is returned as it is.
func (r *Runner) reachStore(ctx context.Context, sessionID string) error {
	// An after position past any Record's end reads no Entries.
	_, err := r.store.Log(ctx, sessionID, math.MaxInt64, 1)
	return err
}

// checkWriter answers not_writer when another live Instance has a Run going on
// the Session. A Session that is idle, or whose writer is gone, or that does
// not exist, is for the caller to run, take over, or find missing.
func (r *Runner) checkWriter(ctx context.Context, sessionID string) error {
	r.warmMu.Lock()
	entry := r.warm[sessionID]
	r.warmMu.Unlock()
	if entry != nil {
		// A warm copy that is current says no other Instance has written since.
		if stale, err := entry.writer.Stale(ctx); err == nil && !stale {
			return nil
		}
	}
	record, err := session.Open(ctx, r.store, sessionID)
	if err != nil {
		return err
	}
	claim, busy := record.Holder()
	if !busy || claim.Instance == "" || claim.Instance == r.instance {
		return nil
	}
	state, err := r.store.Instance(ctx, claim.Instance)
	if err != nil {
		return err
	}
	if !state.Alive {
		return nil
	}
	r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "not_writer"}, "")
	return notWriter(claim.Instance, state.Address)
}
