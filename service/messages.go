package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// MessageKind says where a message takes effect in a Run.
type MessageKind string

const (
	// MessageKindSteer is read on the Run's next model call, at the next Turn
	// boundary.
	MessageKindSteer MessageKind = "steer"
	// MessageKindFollowUp is picked up where the Run would otherwise settle, so
	// the same Run goes on.
	MessageKindFollowUp MessageKind = "follow_up"
	// MessageKindInterrupt cancels the model call in flight, lets a running tool
	// finish, and starts the next Turn of the same Run with the message.
	MessageKindInterrupt MessageKind = "interrupt"
)

// Valid reports whether k is one of the kinds above.
func (k MessageKind) Valid() bool {
	return k == MessageKindSteer || k == MessageKindFollowUp || k == MessageKindInterrupt
}

// MessageRequest is one message for a Session.
type MessageRequest struct {
	SessionID string
	// Kind defaults to MessageKindSteer.
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
	// is Run. Run is nil when that Run was refused before it ran.
	Queued bool         `json:"queued"`
	Run    *RunResponse `json:"run,omitempty"`
}

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
//   - When a Run has ended and not yet released the Session (run_ended is
//     emitted first), Send waits for the release, bounded by ctx, and then
//     acts as below: the message is not answered busy.
//   - When no Run is going, the message starts a new Run on this Instance,
//     which takes the Session over like any request, and Send returns when
//     that Run settles, as Run does. The result holds the Run's.
//
// A message Send reported as queued is never lost because its Run ended. If
// the Run does not take it before it settles, the Run's goroutine starts a
// following Run on the same Session and Instance with every message the Run
// left, in the order they were accepted (the first is its prompt, the rest
// wait in its queues; an Interrupt left over is a Steer). That Run holds the
// Session lock like a Run that waits for it, whatever the busy policy, and
// counts as the admitted Run, so a drain waits for it. Its result is not
// returned to anyone: it is in the Session's stream, the Instance's stream and
// the Record, and the message's ID is the one in its message_committed. The
// leftovers are dropped instead, with a message_dropped Event, only when the
// Run was stopped or canceled (run_ended), the instance is draining
// (draining), or the Run's writer failed (writer_failed); a leftover that does
// not fit the next Run's queues says queue_full, and one whose following Run
// fails before its live Session takes it says run_failed (the store failed to
// open the Session, the Session was deleted, its Agent is gone, the writer was
// fenced). That drop is in the Record when the following Run opened it and can
// still write; when nothing can be written, it is on the Instance's stream
// alone and not stored. A writer that dies loses them, as it loses any
// accepted message.
func (r *Runner) Send(ctx context.Context, request MessageRequest) (MessageResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Kind == "" {
		request.Kind = MessageKindSteer
	}
	switch {
	case strings.TrimSpace(request.SessionID) == "":
		return MessageResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: session ID is required"))
	case !request.Kind.Valid():
		return MessageResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: unknown message kind "+string(request.Kind)))
	case strings.TrimSpace(request.Text) == "":
		return MessageResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: message text is required"))
	}
	result := MessageResult{SessionID: request.SessionID}
	message := gotato.UserMessage(request.Text)
	message.ID = newMessageID()
	waited := false
	for attempt := 0; ; attempt++ {
		if attempt > 0 && r.onSendRetry != nil {
			r.onSendRetry()
		}
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
			// The live Session refuses the message while it is still
			// attached, such as an Interrupt after the Run settled: wait for
			// it to be withdrawn instead of asking again at once.
			if err := handle.awaitLiveChange(ctx); err != nil {
				return result, err
			}
		}
		// No Run takes the message now. A Run that has stopped taking
		// messages still holds the Session until it has saved and released
		// it: wait for that, then look again, so the message is never
		// answered busy for a Run that is only ending.
		ended, err := r.endingRun(ctx, request.SessionID)
		if err != nil {
			return result, err
		}
		if !ended {
			break
		}
		waited = true
	}
	if err := r.checkWriter(ctx, request.SessionID); err != nil {
		return result, err
	}
	run, err := r.runWith(ctx, RunRequest{SessionID: request.SessionID, Prompt: request.Text}, nil, waited)
	// A Run that was refused before it ran (a missing Session, a drain, a full
	// Instance) has no response to give: Run stays nil.
	if err == nil || run.SessionID != "" {
		result.Run = &run
	}
	return result, err
}

// endingRun waits for a Run of the Session on this Instance that no longer
// takes messages, until it has released the Session, bounded by ctx. It
// reports whether there was one.
func (r *Runner) endingRun(ctx context.Context, sessionID string) (bool, error) {
	r.mu.Lock()
	handles := append([]*runHandle(nil), r.handles[sessionID]...)
	r.mu.Unlock()
	for _, handle := range handles {
		if handle.hasLive() {
			// A Run that takes messages again, such as the one that follows.
			return true, nil
		}
		select {
		case <-handle.over:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return false, nil
}

// gone reports a live Session that has no Run to take a message any more.
func gone(err error) bool {
	return gotato.IsCode(err, gotato.CodeSessionClosing) || gotato.IsCode(err, gotato.CodeSessionClosed) || gotato.IsCode(err, gotato.CodeInvalidState)
}

func newMessageID() gotato.MessageID {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return gotato.MessageID("message-" + hex.EncodeToString(buf[:]))
}

// liveRun returns the Run going on this Instance for a Session that has a live
// Session to take a message, waiting for a Run that is still loading its
// Session to have one. It returns nil when no Run is going here.
//
// Before it returns a Run, it reads the Session's Record with the request's
// ctx (reachStore).
func (r *Runner) liveRun(ctx context.Context, sessionID string) (*runHandle, error) {
	handle, err := r.findLiveRun(ctx, sessionID)
	if err != nil || handle == nil {
		return nil, err
	}
	if err := r.reachStore(ctx, sessionID); err != nil {
		return nil, err
	}
	return handle, nil
}

func (r *Runner) findLiveRun(ctx context.Context, sessionID string) (*runHandle, error) {
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
	case <-handles[0].readyChan():
		if handles[0].hasLive() {
			return handles[0], nil
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
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
