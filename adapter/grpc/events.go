// Code in this file maps each Go Event type onto its message of the Event oneof.
package grpcadapter

import (
	"encoding/json"
	"errors"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotatov1 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v1"
)

var errUnsupportedEvent = errors.New("the Event has no wire form")

func wireTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// KindOf returns the kind of an Event on the wire, such as "run_started": the
// name of the member of its oneof, which is the kind in JSON as well. It is
// empty for an Event with no member set.
func KindOf(event *gotatov1.Event) string {
	reflected := event.ProtoReflect()
	field := reflected.WhichOneof(reflected.Descriptor().Oneofs().ByName("event"))
	if field == nil {
		return ""
	}
	return string(field.Name())
}

func headerOf(h gotato.Header) *gotatov1.EventHeader {
	return &gotatov1.EventHeader{Time: wireTime(h.Time), SessionId: string(h.SessionID), Number: h.Number, RunId: string(h.RunID), Turn: uint32(h.Turn)}
}

func messageJSON(m gotato.Message) ([]byte, error) { return json.Marshal(m) }

func messagesJSON(ms []gotato.Message) ([][]byte, error) {
	out := make([][]byte, len(ms))
	for i, m := range ms {
		data, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		out[i] = data
	}
	return out, nil
}

func usageOf(u gotato.Usage) *gotatov1.Usage {
	return &gotatov1.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens}
}

func compactionOf(c *gotato.Compaction) *gotatov1.Compaction {
	if c == nil {
		return nil
	}
	return &gotatov1.Compaction{
		At: wireTime(c.At), ReplacedMessages: int32(c.ReplacedMessages), FromMessageId: string(c.FromMessageID), ToMessageId: string(c.ToMessageID),
		SummaryMessageId: string(c.SummaryMessageID), Recap: c.Recap, BytesBefore: int32(c.BytesBefore), BytesAfter: int32(c.BytesAfter),
		Segment: int32(c.Segment), TokensBefore: int32(c.TokensBefore), TokensAfter: int32(c.TokensAfter), Ceiling: int32(c.Ceiling),
		RecapUsage: recapUsageOf(c.RecapUsage), Tools: c.Tools, Merged: segmentNumbers(c.Merged),
	}
}

func recapUsageOf(u *gotato.Usage) *gotatov1.Usage {
	if u == nil {
		return nil
	}
	return usageOf(*u)
}

func segmentNumbers(numbers []int) []int32 {
	out := make([]int32, len(numbers))
	for i, number := range numbers {
		out[i] = int32(number)
	}
	return out
}

func positionsOf(positions []gotato.MessagePosition) []*gotatov1.MessagePosition {
	out := make([]*gotatov1.MessagePosition, len(positions))
	for i, p := range positions {
		out[i] = &gotatov1.MessagePosition{Id: string(p.ID), Number: p.Number, Time: wireTime(p.Time)}
	}
	return out
}

func sourceOf(s gotato.SegmentSource) *gotatov1.SegmentSource {
	return &gotatov1.SegmentSource{SessionId: string(s.SessionID), FromNumber: s.FromNumber, ToNumber: s.ToNumber}
}

// segmentOf is the wire form of a Segment, with its rendered Messages as JSON.
func segmentOf(s gotato.Segment) (*gotatov1.Segment, error) {
	messages, err := messagesJSON(s.Messages)
	if err != nil {
		return nil, err
	}
	lines := make([]*gotatov1.ToolCallLine, len(s.ToolCalls))
	for i, line := range s.ToolCalls {
		lines[i] = &gotatov1.ToolCallLine{At: wireTime(line.At), ToolId: line.ToolID, Args: line.Args, Status: string(line.Status), Code: line.Code, Count: int32(line.Count)}
	}
	var merged []*gotatov1.MergedSegment
	for _, m := range s.Merged {
		merged = append(merged, &gotatov1.MergedSegment{Number: int32(m.Number), From: wireTime(m.From), To: wireTime(m.To), Source: sourceOf(m.Source)})
	}
	return &gotatov1.Segment{
		Number: int32(s.Number), From: wireTime(s.From), To: wireTime(s.To),
		Source:       sourceOf(s.Source),
		MessagesJson: messages, Rendered: int32(s.Rendered), ToolCalls: lines, Recap: s.Recap, Tools: s.Tools, Merged: merged,
	}, nil
}

func compactionsOf(cs []gotato.Compaction) []*gotatov1.Compaction {
	out := make([]*gotatov1.Compaction, len(cs))
	for i := range cs {
		out[i] = compactionOf(&cs[i])
	}
	return out
}

// eventOf converts an Event to its wire form: the header, and the message of
// its own type in the oneof.
func eventOf(event gotato.Event) (*gotatov1.Event, error) {
	wire := &gotatov1.Event{Header: headerOf(event.Head())}
	fail := func(err error) (*gotatov1.Event, error) {
		return nil, status.Error(codes.Internal, "cannot encode Event: "+err.Error())
	}
	switch e := event.(type) {
	case gotato.RunStarted:
		wire.Event = &gotatov1.Event_RunStarted{RunStarted: &gotatov1.RunStarted{}}
	case gotato.RunEnded:
		wire.Event = &gotatov1.Event_RunEnded{RunEnded: &gotatov1.RunEnded{
			Status:             string(e.Status),
			Cause:              string(e.Cause),
			StoppedByExtension: e.StoppedByExtension,
			StopReason:         e.StopReason,
			Turns:              e.Turns,
			ToolCalls:          e.ToolCalls,
			Usage:              usageOf(e.Usage),
			ElapsedMs:          e.ElapsedMS,
			TextBytes:          e.TextBytes,
			ReasoningBytes:     e.ReasoningBytes,
		}}
	case gotato.TurnStarted:
		wire.Event = &gotatov1.Event_TurnStarted{TurnStarted: &gotatov1.TurnStarted{}}
	case gotato.TurnEnded:
		tools := make([]*gotatov1.ToolOutcome, len(e.Tools))
		for i, t := range e.Tools {
			tools[i] = &gotatov1.ToolOutcome{ToolId: t.ToolID, CallId: string(t.CallID), Status: string(t.Status), Executed: t.Executed}
		}
		wire.Event = &gotatov1.Event_TurnEnded{TurnEnded: &gotatov1.TurnEnded{
			StopReason:     string(e.StopReason),
			Usage:          usageOf(e.Usage),
			ElapsedMs:      e.ElapsedMS,
			TextBytes:      e.TextBytes,
			ReasoningBytes: e.ReasoningBytes,
			ToolCalls:      int32(e.ToolCalls),
			Tools:          tools,
		}}
	case gotato.ContextBuilt:
		wire.Event = &gotatov1.Event_ContextBuilt{ContextBuilt: &gotatov1.ContextBuilt{
			Strategy:         e.Strategy,
			Messages:         int32(e.Messages),
			SourceMessages:   int32(e.SourceMessages),
			SelectedMessages: int32(e.SelectedMessages),
			PrefixHash:       e.PrefixHash,
			PrefixMessages:   int32(e.PrefixMessages),
			SystemBytes:      int32(e.SystemBytes),
			Tools:            int32(e.Tools),
			Metadata:         e.Metadata,
		}}
	case gotato.ReplyStarted:
		wire.Event = &gotatov1.Event_ReplyStarted{ReplyStarted: &gotatov1.ReplyStarted{
			MessageId: string(e.MessageID),
		}}
	case gotato.ReplyEnded:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_ReplyEnded{ReplyEnded: &gotatov1.ReplyEnded{
			MessageJson: message,
			Model:       e.Model,
			StopReason:  string(e.StopReason),
			ToolCalls:   int32(e.ToolCalls),
		}}
	case gotato.TextDelta:
		wire.Event = &gotatov1.Event_TextDelta{TextDelta: &gotatov1.TextDelta{
			MessageId: string(e.MessageID),
			Text:      e.Text,
		}}
	case gotato.ReasoningDelta:
		wire.Event = &gotatov1.Event_ReasoningDelta{ReasoningDelta: &gotatov1.ReasoningDelta{
			MessageId: string(e.MessageID),
			Text:      e.Text,
		}}
	case gotato.ReplyCutOff:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_ReplyCutOff{ReplyCutOff: &gotatov1.ReplyCutOff{
			MessageJson: message,
			Cause:       string(e.Cause),
		}}
	case gotato.ReplyPartKept:
		wire.Event = &gotatov1.Event_ReplyPartKept{ReplyPartKept: &gotatov1.ReplyPartKept{
			MessageId: string(e.MessageID),
			Text:      e.Text,
			Reasoning: e.Reasoning,
		}}
	case gotato.ModelRetried:
		wire.Event = &gotatov1.Event_ModelRetried{ModelRetried: &gotatov1.ModelRetried{
			Attempt: int32(e.Attempt),
			WaitMs:  e.WaitMS,
			Error:   e.Error,
		}}
	case gotato.ToolStarted:
		wire.Event = &gotatov1.Event_ToolStarted{ToolStarted: &gotatov1.ToolStarted{
			MessageId: string(e.MessageID),
			CallId:    string(e.CallID),
			ToolId:    e.ToolID,
		}}
	case gotato.ToolEnded:
		wire.Event = &gotatov1.Event_ToolEnded{ToolEnded: &gotatov1.ToolEnded{
			CallId:    string(e.CallID),
			ToolId:    e.ToolID,
			Status:    string(e.Status),
			Executed:  e.Executed,
			ElapsedMs: e.ElapsedMS,
		}}
	case gotato.ToolProgress:
		wire.Event = &gotatov1.Event_ToolProgress{ToolProgress: &gotatov1.ToolProgress{
			CallId: string(e.CallID),
			Text:   e.Text,
		}}
	case gotato.ToolResultCommitted:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_ToolResultCommitted{ToolResultCommitted: &gotatov1.ToolResultCommitted{
			MessageJson: message,
			CallId:      string(e.CallID),
			Status:      string(e.Status),
		}}
	case gotato.ToolResultSpilled:
		wire.Event = &gotatov1.Event_ToolResultSpilled{ToolResultSpilled: &gotatov1.ToolResultSpilled{
			CallId: string(e.CallID),
			ToolId: e.ToolID,
			Bytes:  int64(e.Bytes),
			Body:   e.Body,
		}}
	case gotato.ToolLoaded:
		wire.Event = &gotatov1.Event_ToolLoaded{ToolLoaded: &gotatov1.ToolLoaded{
			Tool: e.Tool,
			Why:  string(e.Why),
		}}
	case gotato.ToolUnloaded:
		wire.Event = &gotatov1.Event_ToolUnloaded{ToolUnloaded: &gotatov1.ToolUnloaded{
			Tool: e.Tool,
			Why:  string(e.Why),
		}}
	case gotato.RunRequested:
		wire.Event = &gotatov1.Event_RunRequested{RunRequested: &gotatov1.RunRequested{
			Agent: e.Agent,
		}}
	case gotato.RunWaiting:
		wire.Event = &gotatov1.Event_RunWaiting{RunWaiting: &gotatov1.RunWaiting{
			Why: e.Why,
		}}
	case gotato.RunRejected:
		wire.Event = &gotatov1.Event_RunRejected{RunRejected: &gotatov1.RunRejected{
			Why: e.Why,
		}}
	case gotato.MessageQueued:
		wire.Event = &gotatov1.Event_MessageQueued{MessageQueued: &gotatov1.MessageQueued{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
		}}
	case gotato.MessageCommitted:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_MessageCommitted{MessageCommitted: &gotatov1.MessageCommitted{
			MessageJson: message,
			Source:      string(e.Source),
		}}
	case gotato.MessageDropped:
		wire.Event = &gotatov1.Event_MessageDropped{MessageDropped: &gotatov1.MessageDropped{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
			Why:       e.Why,
		}}
	case gotato.NoticeQueued:
		wire.Event = &gotatov1.Event_NoticeQueued{NoticeQueued: &gotatov1.NoticeQueued{
			MessageId:  string(e.MessageID),
			NoticeKind: e.NoticeKind,
			Source:     string(e.Source),
			Text:       e.Text,
		}}
	case gotato.NoticeCommitted:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_NoticeCommitted{NoticeCommitted: &gotatov1.NoticeCommitted{
			MessageJson: message,
			Queued:      e.Queued,
		}}
	case gotato.MessageRejected:
		wire.Event = &gotatov1.Event_MessageRejected{MessageRejected: &gotatov1.MessageRejected{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
			Why:       e.Why,
		}}
	case gotato.AbortRequested:
		wire.Event = &gotatov1.Event_AbortRequested{AbortRequested: &gotatov1.AbortRequested{}}
	case gotato.SessionClaimed:
		wire.Event = &gotatov1.Event_SessionClaimed{SessionClaimed: &gotatov1.SessionClaimed{
			Instance: e.Instance,
			Epoch:    e.Epoch,
			Why:      string(e.Why),
			From:     e.From,
		}}
	case gotato.SessionReleased:
		wire.Event = &gotatov1.Event_SessionReleased{SessionReleased: &gotatov1.SessionReleased{
			Instance: e.Instance,
			Epoch:    e.Epoch,
		}}
	case gotato.SessionFenced:
		wire.Event = &gotatov1.Event_SessionFenced{SessionFenced: &gotatov1.SessionFenced{
			Instance: e.Instance,
			Position: e.Position,
		}}
	case gotato.LivenessLapsed:
		wire.Event = &gotatov1.Event_LivenessLapsed{LivenessLapsed: &gotatov1.LivenessLapsed{
			Instance: e.Instance,
		}}
	case gotato.SessionCreated:
		var forked *gotatov1.ForkedFrom
		if e.ForkedFrom != nil {
			forked = &gotatov1.ForkedFrom{SessionId: string(e.ForkedFrom.SessionID), Number: e.ForkedFrom.Number}
		}
		messages, err := messagesJSON(e.Messages)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_SessionCreated{SessionCreated: &gotatov1.SessionCreated{
			RecordEpoch:  e.RecordEpoch,
			CreatedAt:    wireTime(e.CreatedAt),
			ForkedFrom:   forked,
			MessagesJson: messages,
			Metadata:     e.Metadata,
			Compactions:  compactionsOf(e.Compactions),
			Tools:        e.Tools,
			Segments:     segmentNumbers(e.Segments),
			Tail:         positionsOf(e.Tail),
		}}
	case gotato.MetadataChanged:
		wire.Event = &gotatov1.Event_MetadataChanged{MetadataChanged: &gotatov1.MetadataChanged{
			Key:   e.Key,
			Value: e.Value,
		}}
	case gotato.CompactionStarted:
		wire.Event = &gotatov1.Event_CompactionStarted{CompactionStarted: &gotatov1.CompactionStarted{
			Why: string(e.Why),
		}}
	case gotato.CompactionEnded:
		messages, err := messagesJSON(e.Messages)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_CompactionEnded{CompactionEnded: &gotatov1.CompactionEnded{
			Why:          string(e.Why),
			Compaction:   compactionOf(e.Compaction),
			History:      compactionsOf(e.History),
			MessagesJson: messages,
			Metadata:     e.Metadata,
			Tools:        e.Tools,
			Segments:     segmentNumbers(e.Segments),
			Tail:         positionsOf(e.Tail),
		}}
	case gotato.SegmentClosed:
		segment, err := segmentOf(e.Segment)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_SegmentClosed{SegmentClosed: &gotatov1.SegmentClosed{Segment: segment}}
	case gotato.CompactionFailed:
		wire.Event = &gotatov1.Event_CompactionFailed{CompactionFailed: &gotatov1.CompactionFailed{
			Why:   string(e.Why),
			Cause: string(e.Cause),
		}}
	case gotato.SessionUnloaded:
		wire.Event = &gotatov1.Event_SessionUnloaded{SessionUnloaded: &gotatov1.SessionUnloaded{
			Why: e.Why,
		}}
	case gotato.SessionDeleted:
		wire.Event = &gotatov1.Event_SessionDeleted{SessionDeleted: &gotatov1.SessionDeleted{}}
	case gotato.SessionLoaded:
		wire.Event = &gotatov1.Event_SessionLoaded{SessionLoaded: &gotatov1.SessionLoaded{
			Messages:  int32(e.Messages),
			ElapsedMs: e.ElapsedMS,
			Failed:    e.Failed,
		}}
	case gotato.SessionSaved:
		wire.Event = &gotatov1.Event_SessionSaved{SessionSaved: &gotatov1.SessionSaved{
			Messages:  int32(e.Messages),
			ElapsedMs: e.ElapsedMS,
			Writes:    int32(e.Writes),
			Failed:    e.Failed,
		}}
	case gotato.StoreWriteFailed:
		wire.Event = &gotatov1.Event_StoreWriteFailed{StoreWriteFailed: &gotatov1.StoreWriteFailed{
			Code: string(e.Code),
		}}
	case gotato.ExtensionFailed:
		wire.Event = &gotatov1.Event_ExtensionFailed{ExtensionFailed: &gotatov1.ExtensionFailed{
			Stage: e.Stage,
			Code:  string(e.Code),
		}}
	case gotato.EventsSkipped:
		wire.Event = &gotatov1.Event_EventsSkipped{EventsSkipped: &gotatov1.EventsSkipped{
			Skipped: e.Skipped,
		}}
	case gotato.UnknownEvent:
		raw, err := json.Marshal(e)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov1.Event_Unknown{Unknown: &gotatov1.UnknownEvent{Kind: string(e.Kind()), RawJson: raw}}
	default:
		return fail(errUnsupportedEvent)
	}
	return wire, nil
}
