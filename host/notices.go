package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// defaultTimeAway is how long a Session must have been idle before a message
// that starts a Run is followed by a time_away notice (F4.4).
const defaultTimeAway = 30 * time.Minute

// NoticeRequest is one notice from your service for a Session.
type NoticeRequest struct {
	SessionID string
	// Kind is your own short label, for your own counting. Gotato keeps it and
	// does not read it; its own kinds (gotato.NoticeContinue and the others)
	// are reserved.
	Kind string
	Text string
}

// NoticeResult says that a notice was accepted.
type NoticeResult struct {
	SessionID string `json:"session_id"`
	// Number is the number of the notice's notice_queued Event in the
	// Session's Record.
	Number uint64 `json:"number"`
	// MessageID names the notice in the Events that follow it
	// (notice_committed, message_dropped).
	MessageID string `json:"message_id"`
}

// Notice tells the model something that is not the user's words (F4.4). It
// queues the notice and returns at once with the number of its notice_queued
// Event; it never waits for the model, starts no Run and keeps none going.
//
//   - When a Run is going on this Instance, the notice goes to its live
//     Session and is committed right before the Run's next model call, after
//     the Tool Results or the prompt that call follows.
//   - When a Run is going on another live Instance, Notice returns not_writer
//     (NotRun, Retryable) with the writer's advertised address, as Send does.
//   - When no Run is going, this Instance writes the notice_queued Event (it
//     takes the Session over for that write, as Compact does). The Event is in
//     the Record, with the notice's text: the Session's next Run, on any
//     Instance, queues the notices the Record holds as queued and not
//     committed, in order, and they go in after its prompt.
//
// A notice a Run left in its queue when it settled, or when it was stopped,
// waits the same way. The Session's queue is bounded by CoreLimits.MaxNotices
// of its Agent.
func (r *Runner) Notice(ctx context.Context, request NoticeRequest) (NoticeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch {
	case strings.TrimSpace(request.SessionID) == "":
		return NoticeResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: session ID is required"))
	case strings.TrimSpace(request.Kind) == "":
		return NoticeResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: notice kind is required"))
	case gotato.ReservedNoticeKind(request.Kind):
		return NoticeResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: notice kind "+request.Kind+" is reserved"))
	case strings.TrimSpace(request.Text) == "":
		return NoticeResult{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: notice text is required"))
	}
	if r.Draining() {
		return NoticeResult{}, ErrDraining
	}
	result := NoticeResult{SessionID: request.SessionID}
	handle, err := r.liveRun(ctx, request.SessionID)
	if err != nil {
		return NoticeResult{}, err
	}
	if handle != nil {
		number, id, err := handle.notice(ctx, request.Kind, request.Text)
		switch {
		case err == nil:
			result.Number, result.MessageID = number, string(id)
			return result, nil
		case !gone(err):
			return NoticeResult{}, err
		}
		// The Run ended as the notice arrived: no Run is going.
	}
	if err := r.checkWriter(ctx, request.SessionID); err != nil {
		return NoticeResult{}, err
	}
	number, id, err := r.noticeBetweenRuns(ctx, request)
	if err != nil {
		return NoticeResult{}, err
	}
	result.Number, result.MessageID = number, string(id)
	return result, nil
}

// notice puts a notice in the live Session's queue, or fails with
// invalid_state when the Run has no live Session any more.
func (h *runHandle) notice(ctx context.Context, kind, text string) (uint64, gotato.MessageID, error) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.live == nil {
		return 0, "", gotato.ErrorOf(gotato.CodeInvalidState, "host: the run is over")
	}
	message := gotato.NewNotice(gotato.NoticeSourceService, kind, text)
	message.ID = newMessageID()
	number, err := h.live.QueueNotice(ctx, message)
	return number, message.ID, err
}

// noticeBetweenRuns writes the notice_queued Event of a notice that finds no
// Run going. The Record keeps it for the Session's next Run.
func (r *Runner) noticeBetweenRuns(ctx context.Context, request NoticeRequest) (uint64, gotato.MessageID, error) {
	var number uint64
	message := gotato.NewNotice(gotato.NoticeSourceService, request.Kind, request.Text)
	message.ID = newMessageID()
	err := r.sessionLock(ctx, request.SessionID, QueuePolicyWait, func() error {
		var err error
		number, err = r.writeNoticeLocked(ctx, request, message.ID)
		return err
	})
	if err != nil {
		return 0, "", err
	}
	return number, message.ID, nil
}

// writeNoticeLocked is noticeBetweenRuns once it holds the Session's lock: it
// opens the Session's Writer, writes the notice_queued Event under the notice
// limit of the Session's Agent, and releases the Writer.
func (r *Runner) writeNoticeLocked(ctx context.Context, request NoticeRequest, id gotato.MessageID) (uint64, error) {
	writer, early, _, err := r.acquire(ctx, request.SessionID)
	if err != nil {
		return 0, err
	}
	agentName, _ := writer.Record().Get(metaAgent)
	r.publishOpened(writer, early, agentName)
	spec, err := r.agentOf(ctx, writer.Record())
	known := err == nil
	if err != nil && !errors.Is(err, ErrUnknownAgent) {
		_ = r.releaseWriter(ctx, writer)
		return 0, err
	}
	limit := int(gotato.DefaultLimits().MaxNotices)
	if known && spec.Limits != nil && spec.Limits.MaxNotices > 0 {
		limit = int(spec.Limits.MaxNotices)
	}
	number, err := writeNoticeQueued(ctx, writer, request, id, limit)
	releaseErr := r.releaseWriter(ctx, writer)
	if known && releaseErr == nil {
		r.keepWarm(writer, spec)
	}
	if err != nil {
		return number, err
	}
	return number, releaseErr
}

// writeNoticeQueued writes the notice_queued Event of a notice, unless the
// Session already holds limit notices, and returns the Event's number.
func writeNoticeQueued(ctx context.Context, writer *store.Writer, request NoticeRequest, id gotato.MessageID, limit int) (uint64, error) {
	var written []gotato.Event
	var err error
	switch {
	case len(writer.Record().PendingNotices()) >= limit:
		err = notRunError(gotato.ErrorOf(gotato.CodeLimitExceeded, "host: the notice queue is full"))
	default:
		written, err = writer.Write(ctx, []gotato.Event{gotato.NoticeQueued{
			Header:    gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0),
			MessageID: id, NoticeKind: request.Kind, Source: gotato.NoticeSourceService, Text: request.Text,
		}})
	}
	// The Writer has sent what it wrote, to the publisher publishOpened set.
	var number uint64
	for _, event := range written {
		if queued, ok := event.(gotato.NoticeQueued); ok {
			number = queued.Number
		}
	}
	return number, err
}

// splitNotices separates the notices from the other messages a Run left.
func splitNotices(left []gotato.Queued) (notices, rest []gotato.Queued) {
	for _, queued := range left {
		if queued.Source == gotato.MessageSourceNotice {
			notices = append(notices, queued)
		} else {
			rest = append(rest, queued)
		}
	}
	return notices, rest
}

// queueTimeAway queues the time_away notice of a message that starts a Run
// after the Session was idle longer than the Agent's threshold (F4.4). The
// idle time is read from the Record: from the end of its last Run to now, so a
// Session that was evicted, or loaded by another Instance, gets the notice the
// same. A Session that has never finished a Run has none.
func (r *Runner) queueTimeAway(ctx context.Context, spec profile.AgentProfile, rec *record.Record, live *gotato.Session) {
	threshold := r.timeAway
	switch {
	case threshold < 0:
		return
	case threshold == 0:
		threshold = defaultTimeAway
	}
	last := rec.LastRunEnd()
	now := r.clock.Now()
	if last.IsZero() || now.Sub(last) <= threshold {
		return
	}
	location := spec.Compact.Location
	if location == nil {
		location = time.UTC
	}
	text := fmt.Sprintf("It has been %s since the last message. It is now %s.", away(now.Sub(last)), now.In(location).Format(time.RFC3339))
	_, _ = live.QueueNotice(ctx, gotato.NewNotice(gotato.NoticeSourceGotato, gotato.NoticeTimeAway, text))
}

// day is the length of a day, the largest unit away names.
const day = 24 * time.Hour

// away says how long the user was away in whole minutes, hours or days: the
// largest unit that fits at least once, and "less than a minute" below that.
func away(d time.Duration) string {
	unit := func(n int64, name string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s", name)
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	switch {
	case d >= day:
		return unit(int64(d/day), "day")
	case d >= time.Hour:
		return unit(int64(d/time.Hour), "hour")
	case d >= time.Minute:
		return unit(int64(d/time.Minute), "minute")
	default:
		return "less than a minute"
	}
}
