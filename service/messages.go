package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// MessageKind says where a message takes effect in a Run.
type MessageKind string

const (
	// MessageSteer is read on the Run's next model call, at the next Turn
	// boundary.
	MessageSteer MessageKind = "steer"
	// MessageFollowUp is picked up where the Run would otherwise settle, so
	// the same Run goes on.
	MessageFollowUp MessageKind = "follow_up"
	// MessageInterrupt cancels the model call in flight, lets a running tool
	// finish, and starts the next Turn of the same Run with the message.
	MessageInterrupt MessageKind = "interrupt"
)

// Valid reports whether k is one of the kinds above.
func (k MessageKind) Valid() bool {
	return k == MessageSteer || k == MessageFollowUp || k == MessageInterrupt
}

// MessageRequest is one message for a Session.
type MessageRequest struct {
	SessionID string
	// Kind defaults to MessageSteer.
	Kind MessageKind
	Text string
}

// MessageResult says what became of a message.
type MessageResult struct {
	SessionID string `json:"session_id"`
	// MessageID names the message in the Events that follow it
	// (message_queued, message_committed, message_dropped), when a Run in
	// flight here took it.
	MessageID string `json:"message_id,omitempty"`
	// Queued says a Run in flight on this Instance took the message. When it
	// is false, no Run was going: the message started a new Run, whose result
	// is Run.
	Queued bool       `json:"queued"`
	Run    *RunResult `json:"run,omitempty"`
}

// ErrNoRun is returned by CancelSession when no Run is going on the Session,
// here or on the Instance that writes it: a Stop finds nothing to stop.
var ErrNoRun = gotato.ErrorOf(gotato.ErrInvalidState, "service: session has no active run")

// notWriter answers a message for a Run that another Instance writes. It was
// not run, and a retry at the writer may succeed. The address is the writer's
// advertised one, absent when it advertises none.
func notWriter(instance, address string) *gotato.RuntimeError {
	e := gotato.ErrorOf(gotato.ErrNotWriter, "service: the session's run is going on another instance")
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
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != gotato.ErrNotWriter {
		return "", false
	}
	return runtimeErr.Details["writer"], true
}

// Send delivers a message to a Session.
//
//   - When a Run is going on this Instance, the message goes to its live
//     Session and takes effect at its own point (MessageKind), in this
//     process. The result is Queued, with the message's ID; the Events that
//     follow it are in the Session's stream.
//   - When a Run is going on another live Instance, the message is not run:
//     Send returns not_writer (NotRun, Retryable) with the writer's
//     advertised address, which WriterOf reads. Sending it there is the
//     caller's job, or the endpoint's when you turn forwarding on.
//   - When no Run is going, the message starts a new Run on this Instance,
//     which takes the Session over like any request, and Send returns when
//     that Run settles, as Run does. The result holds the Run's.
//
// A message that arrives as a Run ends may be dropped instead (a
// message_dropped Event says so).
func (r *Runner) Send(ctx context.Context, request MessageRequest) (MessageResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Kind == "" {
		request.Kind = MessageSteer
	}
	switch {
	case strings.TrimSpace(request.SessionID) == "":
		return MessageResult{}, notRunError(gotato.ErrorOf(gotato.ErrInvalidArgument, "service: session ID is required"))
	case !request.Kind.Valid():
		return MessageResult{}, notRunError(gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown message kind "+string(request.Kind)))
	case strings.TrimSpace(request.Text) == "":
		return MessageResult{}, notRunError(gotato.ErrorOf(gotato.ErrInvalidArgument, "service: message text is required"))
	}
	result := MessageResult{SessionID: request.SessionID}
	message := gotato.UserMessage(request.Text)
	message.ID = newMessageID()
	handle, err := r.liveRun(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	if handle != nil {
		err := handle.send(request.Kind, message)
		switch {
		case err == nil:
			result.MessageID, result.Queued = string(message.ID), true
			return result, nil
		case !gone(err):
			return result, err
		}
		// The Run ended as the message arrived: no Run is going.
	}
	if err := r.checkWriter(ctx, request.SessionID); err != nil {
		return result, err
	}
	run, err := r.Run(ctx, RunRequest{SessionID: request.SessionID, Prompt: request.Text})
	result.Run = &run
	return result, err
}

// attachLive makes the Run's live Session reachable for messages.
func (h *runHandle) attachLive(live *gotato.Session) {
	h.liveMu.Lock()
	h.live = live
	h.liveMu.Unlock()
	h.readyOnce.Do(func() { close(h.ready) })
}

// detachLive withdraws the live Session once its Run has settled. A message
// that was delivered before it, and that the Run did not take, is dropped
// when the Session closes (a message_dropped Event); none is delivered after.
func (h *runHandle) detachLive() {
	h.liveMu.Lock()
	h.live = nil
	h.liveMu.Unlock()
}

// finish says the Run is over, so a message that waited for its live Session
// stops waiting.
func (h *runHandle) finish() { h.readyOnce.Do(func() { close(h.ready) }) }

// hasLive reports whether the Run has a live Session now.
func (h *runHandle) hasLive() bool {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	return h.live != nil
}

// send puts the message in the live Session's queue, or fails with
// invalid_state when the Run has no live Session any more.
func (h *runHandle) send(kind MessageKind, message gotato.Message) error {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	live := h.live
	if live == nil {
		return gotato.ErrorOf(gotato.ErrInvalidState, "service: the run is over")
	}
	switch kind {
	case MessageFollowUp:
		return live.FollowUp(message)
	case MessageInterrupt:
		return live.Interrupt(message)
	default:
		return live.Steer(message)
	}
}

// gone reports a live Session that has no Run to take a message any more.
func gone(err error) bool {
	return gotato.IsCode(err, gotato.ErrSessionClosing) || gotato.IsCode(err, gotato.ErrSessionClosed) || gotato.IsCode(err, gotato.ErrInvalidState)
}

func newMessageID() gotato.MessageID {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return gotato.MessageID("message-" + hex.EncodeToString(buf[:]))
}

// liveRun returns the Run going on this Instance for a Session that has a live
// Session to take a message, waiting for a Run that is still loading its
// Session to have one. It returns nil when no Run is going here.
func (r *Runner) liveRun(ctx context.Context, sessionID string) (*runHandle, error) {
	r.mu.Lock()
	handles := append([]*runHandle(nil), r.handles[sessionID]...)
	r.mu.Unlock()
	for _, handle := range handles {
		if handle.hasLive() {
			return handle, nil
		}
	}
	// A Run that was admitted but has not built its live Session yet is the
	// one that holds the Session lock, or waits for it; the message waits for
	// the first of them, which ends when its Run does.
	if len(handles) == 0 {
		return nil, nil
	}
	select {
	case <-handles[0].ready:
		if handles[0].hasLive() {
			return handles[0], nil
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
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
