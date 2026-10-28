package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
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
	// RequestID is the caller's own identifier of this request, echoed in
	// MessageResult.RequestID. When the message starts a Run it is recorded in
	// that Run's run_started Event, as RunRequest.RequestID is. A message that a
	// Run in flight takes is answered with the RequestID and the message's ID
	// (MessageResult.MessageID), and the Record names the message by that ID
	// alone. Gotato does not deduplicate: a message sent again with the same
	// RequestID is a second message.
	RequestID string
}

// MessageResult says what became of a message.
type MessageResult struct {
	// RequestID echoes MessageRequest.RequestID.
	RequestID string `json:"request_id,omitempty"`
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
// Run was stopped or canceled (run_ended), the Instance is draining
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
	if invalid := request.validate(); invalid != nil {
		return MessageResult{}, notRunError(invalid)
	}
	result := MessageResult{SessionID: request.SessionID, RequestID: request.RequestID}
	message := gotato.UserMessage(request.Text)
	message.ID = newMessageID()
	queued, waited, err := r.queueToLiveRun(ctx, request, message)
	if err != nil {
		return result, err
	}
	if queued {
		result.MessageID, result.Queued = string(message.ID), true
		return result, nil
	}
	if err := r.checkWriter(ctx, request.SessionID); err != nil {
		return result, err
	}
	run, err := r.runWith(ctx, RunRequest{SessionID: request.SessionID, Prompt: request.Text, RequestID: request.RequestID}, nil, waited)
	// A Run that was refused before it ran (a missing Session, a drain, a full
	// Instance) has no response to give: Run stays nil.
	if err == nil || run.SessionID != "" {
		result.Run = &run
	}
	return result, err
}

// validate reports why a message cannot be sent, as an invalid_argument error.
func (m MessageRequest) validate() *gotato.RuntimeError {
	switch {
	case strings.TrimSpace(m.SessionID) == "":
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "service: session ID is required")
	case !m.Kind.Valid():
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "service: unknown message kind "+string(m.Kind))
	case strings.TrimSpace(m.Text) == "":
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "service: message text is required")
	}
	return nil
}

// queueToLiveRun hands the message to the Run in flight on this Instance and
// reports whether a Run took it. When none did, no Run is going, and waited
// says the loop had to wait for a Run that was only ending to release the
// Session.
func (r *Runner) queueToLiveRun(ctx context.Context, request MessageRequest, message gotato.Message) (queued, waited bool, err error) {
	for attempt := 0; ; attempt++ {
		if attempt > 0 && r.onSendRetry != nil {
			r.onSendRetry()
		}
		handle, err := r.liveRun(ctx, request.SessionID)
		if err != nil {
			return false, waited, err
		}
		if handle != nil {
			err := handle.send(ctx, request.Kind, message)
			switch {
			case err == nil:
				return true, waited, nil
			case !gone(err):
				return false, waited, err
			}
			// The live Session refuses the message while it is still
			// attached, such as an Interrupt after the Run settled: wait for
			// it to be withdrawn instead of asking again at once.
			if err := handle.awaitLiveChange(ctx); err != nil {
				return false, waited, err
			}
		}
		// No Run takes the message now. A Run that has stopped taking
		// messages still holds the Session until it has saved and released
		// it: wait for that, then look again, so the message is never
		// answered busy for a Run that is only ending.
		ended, err := r.endingRun(ctx, request.SessionID)
		if err != nil {
			return false, waited, err
		}
		if !ended {
			return false, waited, nil
		}
		waited = true
	}
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
