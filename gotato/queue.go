package gotato

import (
	"cmp"
	"context"
	"slices"
)

// Steer delivers guidance for the Run in flight. The Session commits it at the
// next Turn boundary; it never interrupts the current Model or Tool work. A
// Steer that arrives where the Run would settle keeps the Run going for one
// more Turn rather than being dropped. Its way into the Session is reported
// as Events: MessageQueued, and then MessageCommitted, or MessageDropped when
// the Run ends first, or MessageRejected when the queue is full.
//
// ctx bounds the write of MessageQueued to the Record, and nothing after it.
// A nil error means the message is queued in this Instance's memory: if the
// Instance dies before the Run commits or drops it, the Record holds a
// message_queued with no closing Event, and the message is lost.
func (a *Session) Steer(ctx context.Context, message Message) error {
	return a.enqueueControl(ctx, a.steer, message, "Steer", MessageSourceSteer)
}

// Interrupt delivers a Message that cuts the model call in flight short. The
// call is canceled, the text it had sent is kept as a Cut-off reply, and the
// Message starts the next Turn of the same Run. A Tool in flight is not
// canceled: it finishes, and the Message follows its result at the Turn
// boundary, as a Steer does. It shares the Steer queue and its limit
// (MaxSteerMessages), and is reported by the same Events, with the source
// MessageSourceInterrupt. It fails with invalid_state when no Run is in flight.
// ctx and a nil result mean what they mean for Steer.
func (a *Session) Interrupt(ctx context.Context, message Message) error {
	if !a.inRun.Load() {
		return runtimeError(CodeInvalidState, "Interrupt", "no Run is in flight", nil)
	}
	if err := a.enqueueControl(ctx, a.steer, message, "Interrupt", MessageSourceInterrupt); err != nil {
		return err
	}
	a.intrMu.Lock()
	a.intrPending++
	cancel := a.callCancel
	pending := a.intrPending
	a.intrMu.Unlock()
	// The Message may have been consumed already, at a boundary that fell
	// between the queueing and this count.
	if cancel != nil && pending > 0 {
		cancel()
	}
	return nil
}

// control is a Message that waits in the Steer or FollowUp queue.
type control struct {
	seq     uint64
	message Message
	source  MessageSource
	// number is the notice_queued of a notice.
	number uint64
}

// interruptTaken says an Interrupt left its queue, committed or dropped.
func (a *Session) interruptTaken() {
	a.intrMu.Lock()
	a.intrPending--
	a.intrMu.Unlock()
}

// interrupting reports whether an Interrupt waits in the queue.
func (a *Session) interrupting() bool {
	a.intrMu.Lock()
	defer a.intrMu.Unlock()
	return a.intrPending > 0
}

// FollowUp supplies the continuation to use where the Run would otherwise
// settle. It is not a general external request queue: the buffer is bounded
// and belongs to this Session. ctx and a nil result mean what they mean for
// Steer.
func (a *Session) FollowUp(ctx context.Context, message Message) error {
	return a.enqueueControl(ctx, a.followUp, message, "FollowUp", MessageSourceFollowUp)
}

func (a *Session) enqueueControl(ctx context.Context, target chan control, message Message, operation string, source MessageSource) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateControlMessage(message, operation); err != nil {
		return err
	}
	if err := a.admissionError(operation); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return notRunBeforeAdmission(err, operation)
	}
	message = message.Clone()
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	reject := func(why string, err error) error {
		// A refusal is reported as an Event, written on a best-effort basis:
		// the caller already gets the error.
		_ = a.writeNow(ctx, MessageRejected{Header: a.nowHeader(), MessageID: message.ID, Source: source, Why: why})
		return err
	}
	if len(target) >= cap(target) {
		return reject("queue_full", runtimeError(CodeLimitExceeded, operation, operation+" buffer is full", nil))
	}
	// The message is written as queued before it can be committed, so the
	// Record never shows a commit with no queueing.
	if err := a.writeNow(ctx, MessageQueued{Header: a.nowHeader(), MessageID: message.ID, Source: source}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return runtimeError(codeForContext(ctxErr), operation, operation+" context ended while queueing", ctxErr)
		}
		return err
	}
	select {
	case target <- control{message: message, source: source, seq: a.ctlSeq.Add(1)}:
		return nil
	default:
		return reject("queue_full", runtimeError(CodeLimitExceeded, operation, operation+" buffer is full", nil))
	}
}

// WithQueueHandoff makes the Session keep the messages (Steer, FollowUp,
// Interrupt) still queued when a Run completes or fails, instead of dropping
// them at its end, so the host can take them with TakeQueued and start the
// next Run with them. A Run that is canceled, or ends on its deadline, still
// drops them. A host that sets it must call TakeQueued, or DropQueued, after
// every Run; Close drops what is left (message_dropped, session_closed).
func WithQueueHandoff() SessionOption {
	return func(c *sessionConfig) error { c.handoff = true; return nil }
}

// keepsQueued says whether a Run that ends with status leaves its queues for
// the host.
func (a *Session) keepsQueued(status RunStatus) bool {
	return a.handoff && (status == RunStatusCompleted || status == RunStatusFailed)
}

// Queued is a message that waited in a queue when a Run ended.
type Queued struct {
	Message Message
	Source  MessageSource
	seq     uint64
	number  uint64
}

// queuedNotice returns the queue entry of a notice that was reported queued
// already, as number, the notice_queued Event that said so. A host that keeps a
// notice between Runs gives it to the next Session with Requeue.
func queuedNotice(message Message, number uint64) Queued {
	return Queued{Message: message, Source: MessageSourceNotice, number: number}
}

// Number is the notice_queued Event of a queued notice, 0 for any other
// message.
func (q Queued) Number() uint64 { return q.number }

// TakeQueued removes and returns the messages that wait in the Steer and
// FollowUp queues, in the order they were accepted. It is for a host that set
// WithQueueHandoff, between Runs: it takes them without a drop Event, and the
// host starts the next Run with them (Requeue) or reports them (DropQueued).
func (a *Session) TakeQueued() []Queued {
	var out []Queued
	for _, queue := range []chan control{a.steer, a.followUp} {
		for {
			select {
			case queued := <-queue:
				if queued.source == MessageSourceInterrupt {
					a.interruptTaken()
				}
				out = append(out, Queued{Message: queued.message, Source: queued.source, seq: queued.seq})
				continue
			default:
			}
			break
		}
	}
	for _, queued := range a.takeNotices() {
		out = append(out, Queued{Message: queued.message, Source: MessageSourceNotice, seq: queued.seq, number: queued.number})
	}
	slices.SortFunc(out, func(a, b Queued) int { return cmp.Compare(a.seq, b.seq) })
	return out
}

// Requeue puts messages taken from another Session's queues into this one's
// queues, in order, without Events: they were reported as queued already. An
// Interrupt becomes a Steer, since no model call is in flight to cut short.
// They keep the order they had: they go into the Steer queue while it has
// room and then into the FollowUp queue, and a Run takes the Steer queue
// before the FollowUp queue. The queue limits apply; it returns the messages
// that did not fit.
func (a *Session) Requeue(messages []Queued) (rest []Queued) {
	for _, m := range messages {
		source := m.Source
		if source == MessageSourceInterrupt {
			source = MessageSourceSteer
		}
		queued := control{message: m.Message, source: source, seq: a.ctlSeq.Add(1), number: m.number}
		if source == MessageSourceNotice {
			a.noticeMu.Lock()
			fits := len(a.notices)+a.noticeHeld < a.noticeLimit
			if fits {
				a.notices = append(a.notices, queued)
			}
			a.noticeMu.Unlock()
			if !fits {
				rest = append(rest, m)
			}
			continue
		}
		select {
		case a.steer <- queued:
		default:
			select {
			case a.followUp <- queued:
			default:
				rest = append(rest, m)
			}
		}
	}
	return rest
}

// DropQueued reports messages that will not run: a message_dropped Event for
// each, with why.
func (a *Session) DropQueued(messages []Queued, why string) {
	for _, m := range messages {
		_ = a.writeNow(context.Background(), MessageDropped{Header: a.nowHeader(), MessageID: m.Message.ID, Source: m.Source, Why: why})
	}
}
