package gotato

import (
	"context"
	"strings"

	"github.com/jinhuang712/gotato/internal/text"
)

// The kinds of notice Gotato sends itself. They are reserved: a service names
// its own kinds, and Session.Notice refuses these.
const (
	// NoticeContinue: the last reply did not finish, because it hit the
	// output limit, so go on.
	NoticeContinue = "continue"
	// NoticeTimeAway: the user was away; how long it has been, and the time now.
	NoticeTimeAway = "time_away"
)

// ReservedNoticeKind reports whether kind is one of Gotato's own.
func ReservedNoticeKind(kind string) bool {
	switch kind {
	case NoticeContinue, NoticeTimeAway:
		return true
	}
	return false
}

// PendingNotice is a notice that was queued and not yet committed or dropped:
// what a Record keeps of it, so the next Run on any instance can queue it
// again (session.Record.PendingNotices).
type PendingNotice struct {
	MessageID MessageID    `json:"message_id"`
	Kind      string       `json:"kind"`
	Source    NoticeSource `json:"source"`
	Text      string       `json:"text"`
	// Number is the number of its notice_queued Event.
	Number uint64 `json:"number"`
}

// Queued returns the queue entry of the pending notice, for Requeue.
func (p PendingNotice) Queued() Queued {
	message := NewNotice(p.Source, p.Kind, p.Text)
	message.ID = p.MessageID
	return queuedNotice(message, p.Number)
}

// NewNotice returns a notice Message holding text. The text is kept as it is;
// ForModel writes it into the request as <notice kind="…">text</notice>.
func NewNotice(source NoticeSource, kind, text string) Message {
	return Message{
		Role:   RoleNotice,
		Parts:  []ContentPart{{Kind: ContentKindText, Text: text}},
		Notice: &NoticeInfo{Kind: kind, Source: source},
	}
}

// renderNotice is the tagged text a notice reaches the model as. The kind and
// the text are escaped, so neither can close the tag or open another.
func renderNotice(message Message) string {
	kind := ""
	if message.Notice != nil {
		kind = message.Notice.Kind
	}
	kind = strings.NewReplacer(`"`, "&quot;", "<", "&lt;", ">", "&gt;", "\n", " ").Replace(kind)
	return "<" + TagNotice + ` kind="` + kind + `">` + text.EscapeTags(TextOf(message)) + "</" + TagNotice + ">"
}

// Notice queues a notice from your service or an extension and returns the
// number of its notice_queued Event; it never waits for the model. The notice
// goes into the Transcript right before the next model call, after the Tool
// Results or the prompt that call follows, in the order it arrived among
// Steers. It starts no Run and no Round, and never keeps a Run going: with no
// Run, or a Run that settles first, it waits for the next one. kind is your
// own label, which Gotato keeps and does not read; Gotato's own kinds are
// reserved and refused. The queue is bounded by CoreLimits.MaxNotices.
func (a *Session) Notice(ctx context.Context, kind, text string) (uint64, error) {
	if ReservedNoticeKind(kind) {
		return 0, runtimeError(CodeInvalidArgument, "Notice", "notice kind "+kind+" is reserved", nil)
	}
	return a.QueueNotice(ctx, NewNotice(NoticeSourceService, kind, text))
}

// QueueNotice queues a notice Message, of any source and kind, as Notice does.
// It is for the host that speaks for Gotato, such as the service layer's
// time_away; a service's own notice goes through Notice.
func (a *Session) QueueNotice(ctx context.Context, message Message) (uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if message.Role != RoleNotice || message.Notice == nil {
		return 0, runtimeError(CodeInvalidArgument, "Notice", "a notice needs the role notice and its info", nil)
	}
	if strings.TrimSpace(message.Notice.Kind) == "" {
		return 0, runtimeError(CodeInvalidArgument, "Notice", "notice kind is empty", nil)
	}
	if !hasContent(message) {
		return 0, runtimeError(CodeInvalidArgument, "Notice", "notice is empty", nil)
	}
	if err := a.admissionError("Notice"); err != nil {
		return 0, err
	}
	message = message.Clone()
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	reject := func(why string, err error) (uint64, error) {
		_ = a.writeNow(context.Background(), MessageRejected{Header: a.nowHeader(), MessageID: message.ID, Source: MessageSourceNotice, Why: why})
		return 0, err
	}
	// [CONTRACT] a Notice that is told "queued" must not be refused later at the boundary for its size.
	if err := a.controlSizeError(message, "Notice"); err != nil {
		return reject("too_large", err)
	}
	// The place is reserved before the Event is written, and held meanwhile.
	if !a.queue.reserve(kindNotice) {
		return reject("queue_full", runtimeError(CodeLimitExceeded, "Notice", "notice queue is full", nil))
	}
	written, werr := a.writeEvents(ctx, []Event{NoticeQueued{
		Header: a.nowHeader(), MessageID: message.ID, NoticeKind: message.Notice.Kind,
		Source: message.Notice.Source, Text: TextOf(message),
	}})
	if werr != nil {
		a.queue.release(kindNotice)
		return 0, werr
	}
	number := written[0].Head().Number
	// [LIVENESS] a Notice queued after the last drop pass would wait in the queue forever.
	if !a.queue.fill(control{message: message, source: MessageSourceNotice, kind: kindNotice, number: number}) {
		// The Session closed while the notice was being written: no pass
		// is left to drop it.
		return 0, a.dropAfterQueued(message.ID, MessageSourceNotice, "session_closed", runtimeError(CodeSessionClosed, "Notice", "Session is closed", nil))
	}
	return number, nil
}

// commitNotice commits a notice Message and stores its NoticeCommitted. number
// is the notice_queued it came from, and causedBy the Event that caused a
// notice of Gotato's own, each 0 when there is none.
func (a *Session) commitNotice(ctx context.Context, runID RunID, turn TurnNumber, message Message, number, causedBy uint64) *RuntimeError {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message.RunID = runID
	if causedBy != 0 && message.Notice != nil {
		info := *message.Notice
		info.CausedBy = causedBy
		message.Notice = &info
	}
	if err := a.commitMessage(message); err != nil {
		return asRuntimeError(err)
	}
	a.stage(NoticeCommitted{Header: NewHeader(a.id, runID, turn), Message: message.Clone(), Queued: number})
	return a.flush(ctx)
}

// commitQueuedNotices commits every notice that waits, in order: right before
// a model call.
func (a *Session) commitQueuedNotices(ctx context.Context, runID RunID, turn TurnNumber) *RuntimeError {
	for _, queued := range a.queue.takeNotices() {
		if err := a.commitNotice(ctx, runID, turn, queued.message, queued.number, 0); err != nil {
			return err
		}
	}
	return nil
}
