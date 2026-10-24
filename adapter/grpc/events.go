// Code in this file maps each Go Event type onto its message of the Event oneof.
package grpcadapter

import (
	"encoding/json"
	"errors"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotatov2 "github.com/jinhuang712/gotato/adapter/grpc/gotato/v2"
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
func KindOf(event *gotatov2.Event) string {
	reflected := event.ProtoReflect()
	field := reflected.WhichOneof(reflected.Descriptor().Oneofs().ByName("event"))
	if field == nil {
		return ""
	}
	return string(field.Name())
}

func headerOf(h gotato.Header) *gotatov2.EventHeader {
	return &gotatov2.EventHeader{Time: wireTime(h.Time), SessionId: string(h.SessionID), Number: h.Number, RunId: string(h.RunID), Turn: uint32(h.Turn)}
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

func usageOf(u gotato.Usage) *gotatov2.Usage {
	return &gotatov2.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens}
}

func compactionOf(c *gotato.Compaction) *gotatov2.Compaction {
	if c == nil {
		return nil
	}
	return &gotatov2.Compaction{
		At: wireTime(c.At), ReplacedMessages: int32(c.ReplacedMessages), FromMessageId: string(c.FromMessageID), ToMessageId: string(c.ToMessageID),
		SummaryMessageId: string(c.SummaryMessageID), Summarizer: c.Summarizer, BytesBefore: int32(c.BytesBefore), BytesAfter: int32(c.BytesAfter),
		Segment: int32(c.Segment), TokensBefore: int32(c.TokensBefore), TokensAfter: int32(c.TokensAfter), Ceiling: int32(c.Ceiling),
		RecapUsage: recapUsageOf(c.RecapUsage), ToolSets: c.ToolSets,
	}
}

func recapUsageOf(u *gotato.Usage) *gotatov2.Usage {
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

func positionsOf(positions []gotato.MessagePosition) []*gotatov2.MessagePosition {
	out := make([]*gotatov2.MessagePosition, len(positions))
	for i, p := range positions {
		out[i] = &gotatov2.MessagePosition{Id: string(p.ID), Number: p.Number, Time: wireTime(p.Time)}
	}
	return out
}

// segmentOf is the wire form of a Segment, with its rendered Messages as JSON.
func segmentOf(s gotato.Segment) (*gotatov2.Segment, error) {
	messages, err := messagesJSON(s.Messages)
	if err != nil {
		return nil, err
	}
	lines := make([]*gotatov2.ToolCallLine, len(s.ToolCalls))
	for i, line := range s.ToolCalls {
		lines[i] = &gotatov2.ToolCallLine{At: wireTime(line.At), ToolId: line.ToolID, Args: line.Args, Status: string(line.Status), Code: line.Code, Count: int32(line.Count)}
	}
	return &gotatov2.Segment{
		Number: int32(s.Number), From: wireTime(s.From), To: wireTime(s.To),
		Source:       &gotatov2.SegmentSource{SessionId: string(s.Source.SessionID), FromNumber: s.Source.FromNumber, ToNumber: s.Source.ToNumber},
		MessagesJson: messages, Rendered: int32(s.Rendered), ToolCalls: lines, Recap: s.Recap, ToolSets: s.ToolSets,
	}, nil
}

func compactionsOf(cs []gotato.Compaction) []*gotatov2.Compaction {
	out := make([]*gotatov2.Compaction, len(cs))
	for i := range cs {
		out[i] = compactionOf(&cs[i])
	}
	return out
}

// eventOf converts an Event to its wire form: the header, and the message of
// its own type in the oneof.
func eventOf(event gotato.Event) (*gotatov2.Event, error) {
	wire := &gotatov2.Event{Header: headerOf(event.Head())}
	fail := func(err error) (*gotatov2.Event, error) {
		return nil, status.Error(codes.Internal, "cannot encode Event: "+err.Error())
	}
	switch e := event.(type) {
	case gotato.RunStarted:
		wire.Event = &gotatov2.Event_RunStarted{RunStarted: &gotatov2.RunStarted{}}
	case gotato.RunEnded:
		wire.Event = &gotatov2.Event_RunEnded{RunEnded: &gotatov2.RunEnded{
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
		wire.Event = &gotatov2.Event_TurnStarted{TurnStarted: &gotatov2.TurnStarted{}}
	case gotato.TurnEnded:
		tools := make([]*gotatov2.ToolOutcome, len(e.Tools))
		for i, t := range e.Tools {
			tools[i] = &gotatov2.ToolOutcome{ToolId: t.ToolID, CallId: string(t.CallID), Status: string(t.Status), Executed: t.Executed}
		}
		wire.Event = &gotatov2.Event_TurnEnded{TurnEnded: &gotatov2.TurnEnded{
			StopReason:     string(e.StopReason),
			Usage:          usageOf(e.Usage),
			ElapsedMs:      e.ElapsedMS,
			TextBytes:      e.TextBytes,
			ReasoningBytes: e.ReasoningBytes,
			ToolCalls:      int32(e.ToolCalls),
			Tools:          tools,
		}}
	case gotato.ContextBuilt:
		wire.Event = &gotatov2.Event_ContextBuilt{ContextBuilt: &gotatov2.ContextBuilt{
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
		wire.Event = &gotatov2.Event_ReplyStarted{ReplyStarted: &gotatov2.ReplyStarted{
			MessageId: string(e.MessageID),
		}}
	case gotato.ReplyEnded:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_ReplyEnded{ReplyEnded: &gotatov2.ReplyEnded{
			MessageJson: message,
			Model:       e.Model,
			StopReason:  string(e.StopReason),
			ToolCalls:   int32(e.ToolCalls),
		}}
	case gotato.TextDelta:
		wire.Event = &gotatov2.Event_TextDelta{TextDelta: &gotatov2.TextDelta{
			MessageId: string(e.MessageID),
			Text:      e.Text,
		}}
	case gotato.ReasoningDelta:
		wire.Event = &gotatov2.Event_ReasoningDelta{ReasoningDelta: &gotatov2.ReasoningDelta{
			MessageId: string(e.MessageID),
			Text:      e.Text,
		}}
	case gotato.ReplyCutOff:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_ReplyCutOff{ReplyCutOff: &gotatov2.ReplyCutOff{
			MessageJson: message,
			Cause:       string(e.Cause),
		}}
	case gotato.ReplyPartKept:
		wire.Event = &gotatov2.Event_ReplyPartKept{ReplyPartKept: &gotatov2.ReplyPartKept{
			MessageId: string(e.MessageID),
			Text:      e.Text,
			Reasoning: e.Reasoning,
		}}
	case gotato.ModelRetried:
		wire.Event = &gotatov2.Event_ModelRetried{ModelRetried: &gotatov2.ModelRetried{
			Attempt: int32(e.Attempt),
			WaitMs:  e.WaitMS,
			Error:   e.Error,
		}}
	case gotato.ToolStarted:
		wire.Event = &gotatov2.Event_ToolStarted{ToolStarted: &gotatov2.ToolStarted{
			MessageId: string(e.MessageID),
			CallId:    string(e.CallID),
			ToolId:    e.ToolID,
		}}
	case gotato.ToolEnded:
		wire.Event = &gotatov2.Event_ToolEnded{ToolEnded: &gotatov2.ToolEnded{
			CallId:    string(e.CallID),
			ToolId:    e.ToolID,
			Status:    string(e.Status),
			Executed:  e.Executed,
			ElapsedMs: e.ElapsedMS,
		}}
	case gotato.ToolProgress:
		wire.Event = &gotatov2.Event_ToolProgress{ToolProgress: &gotatov2.ToolProgress{
			CallId: string(e.CallID),
			Text:   e.Text,
		}}
	case gotato.ToolResultCommitted:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_ToolResultCommitted{ToolResultCommitted: &gotatov2.ToolResultCommitted{
			MessageJson: message,
			CallId:      string(e.CallID),
			Status:      string(e.Status),
		}}
	case gotato.ToolSetActivated:
		wire.Event = &gotatov2.Event_ToolsetActivated{ToolsetActivated: &gotatov2.ToolSetActivated{
			Toolset: e.ToolSet,
		}}
	case gotato.RunRequested:
		wire.Event = &gotatov2.Event_RunRequested{RunRequested: &gotatov2.RunRequested{
			Agent: e.Agent,
		}}
	case gotato.RunWaiting:
		wire.Event = &gotatov2.Event_RunWaiting{RunWaiting: &gotatov2.RunWaiting{
			Why: e.Why,
		}}
	case gotato.RunRejected:
		wire.Event = &gotatov2.Event_RunRejected{RunRejected: &gotatov2.RunRejected{
			Why: e.Why,
		}}
	case gotato.MessageQueued:
		wire.Event = &gotatov2.Event_MessageQueued{MessageQueued: &gotatov2.MessageQueued{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
		}}
	case gotato.MessageCommitted:
		message, err := messageJSON(e.Message)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_MessageCommitted{MessageCommitted: &gotatov2.MessageCommitted{
			MessageJson: message,
			Source:      string(e.Source),
		}}
	case gotato.MessageDropped:
		wire.Event = &gotatov2.Event_MessageDropped{MessageDropped: &gotatov2.MessageDropped{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
			Why:       e.Why,
		}}
	case gotato.MessageWithdrawn:
		wire.Event = &gotatov2.Event_MessageWithdrawn{MessageWithdrawn: &gotatov2.MessageWithdrawn{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
		}}
	case gotato.MessageRejected:
		wire.Event = &gotatov2.Event_MessageRejected{MessageRejected: &gotatov2.MessageRejected{
			MessageId: string(e.MessageID),
			Source:    string(e.Source),
			Why:       e.Why,
		}}
	case gotato.AbortRequested:
		wire.Event = &gotatov2.Event_AbortRequested{AbortRequested: &gotatov2.AbortRequested{}}
	case gotato.SessionClaimed:
		wire.Event = &gotatov2.Event_SessionClaimed{SessionClaimed: &gotatov2.SessionClaimed{
			Instance: e.Instance,
			Epoch:    e.Epoch,
			Why:      string(e.Why),
			From:     e.From,
		}}
	case gotato.SessionReleased:
		wire.Event = &gotatov2.Event_SessionReleased{SessionReleased: &gotatov2.SessionReleased{
			Instance: e.Instance,
			Epoch:    e.Epoch,
		}}
	case gotato.SessionFenced:
		wire.Event = &gotatov2.Event_SessionFenced{SessionFenced: &gotatov2.SessionFenced{
			Instance: e.Instance,
			Position: e.Position,
		}}
	case gotato.LivenessLapsed:
		wire.Event = &gotatov2.Event_LivenessLapsed{LivenessLapsed: &gotatov2.LivenessLapsed{
			Instance: e.Instance,
		}}
	case gotato.SessionCreated:
		var forked *gotatov2.ForkedFrom
		if e.ForkedFrom != nil {
			forked = &gotatov2.ForkedFrom{SessionId: string(e.ForkedFrom.SessionID), Number: e.ForkedFrom.Number}
		}
		messages, err := messagesJSON(e.Messages)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_SessionCreated{SessionCreated: &gotatov2.SessionCreated{
			RecordEpoch:  e.RecordEpoch,
			CreatedAt:    wireTime(e.CreatedAt),
			ForkedFrom:   forked,
			MessagesJson: messages,
			Metadata:     e.Metadata,
			Compactions:  compactionsOf(e.Compactions),
			ToolSets:     e.ToolSets,
			Segments:     segmentNumbers(e.Segments),
			Tail:         positionsOf(e.Tail),
		}}
	case gotato.MetadataChanged:
		wire.Event = &gotatov2.Event_MetadataChanged{MetadataChanged: &gotatov2.MetadataChanged{
			Key:   e.Key,
			Value: e.Value,
		}}
	case gotato.CompactionStarted:
		wire.Event = &gotatov2.Event_CompactionStarted{CompactionStarted: &gotatov2.CompactionStarted{
			Why: string(e.Why),
		}}
	case gotato.CompactionEnded:
		messages, err := messagesJSON(e.Messages)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_CompactionEnded{CompactionEnded: &gotatov2.CompactionEnded{
			Why:          string(e.Why),
			Compaction:   compactionOf(e.Compaction),
			History:      compactionsOf(e.History),
			MessagesJson: messages,
			Metadata:     e.Metadata,
			ToolSets:     e.ToolSets,
			Segments:     segmentNumbers(e.Segments),
			Tail:         positionsOf(e.Tail),
		}}
	case gotato.SegmentClosed:
		segment, err := segmentOf(e.Segment)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_SegmentClosed{SegmentClosed: &gotatov2.SegmentClosed{Segment: segment}}
	case gotato.CompactionFailed:
		wire.Event = &gotatov2.Event_CompactionFailed{CompactionFailed: &gotatov2.CompactionFailed{
			Why:   string(e.Why),
			Cause: string(e.Cause),
		}}
	case gotato.SessionUnloaded:
		wire.Event = &gotatov2.Event_SessionUnloaded{SessionUnloaded: &gotatov2.SessionUnloaded{
			Why: e.Why,
		}}
	case gotato.SessionDeleted:
		wire.Event = &gotatov2.Event_SessionDeleted{SessionDeleted: &gotatov2.SessionDeleted{}}
	case gotato.SessionLoaded:
		wire.Event = &gotatov2.Event_SessionLoaded{SessionLoaded: &gotatov2.SessionLoaded{
			Messages:  int32(e.Messages),
			ElapsedMs: e.ElapsedMS,
			Failed:    e.Failed,
		}}
	case gotato.SessionSaved:
		wire.Event = &gotatov2.Event_SessionSaved{SessionSaved: &gotatov2.SessionSaved{
			Messages:  int32(e.Messages),
			ElapsedMs: e.ElapsedMS,
			Writes:    int32(e.Writes),
			Failed:    e.Failed,
		}}
	case gotato.StoreWriteFailed:
		wire.Event = &gotatov2.Event_StoreWriteFailed{StoreWriteFailed: &gotatov2.StoreWriteFailed{
			Code: string(e.Code),
		}}
	case gotato.ExtensionFailed:
		wire.Event = &gotatov2.Event_ExtensionFailed{ExtensionFailed: &gotatov2.ExtensionFailed{
			Stage: e.Stage,
			Code:  string(e.Code),
		}}
	case gotato.EventsSkipped:
		wire.Event = &gotatov2.Event_EventsSkipped{EventsSkipped: &gotatov2.EventsSkipped{
			Skipped: e.Skipped,
		}}
	case gotato.UnknownEvent:
		raw, err := json.Marshal(e)
		if err != nil {
			return fail(err)
		}
		wire.Event = &gotatov2.Event_Unknown{Unknown: &gotatov2.UnknownEvent{Kind: string(e.Kind()), RawJson: raw}}
	default:
		return fail(errUnsupportedEvent)
	}
	return wire, nil
}
