package session

import (
	"encoding/json"
	"fmt"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// A Record written before the Event design holds Entries of the kinds below:
// a "checkpoint" fact, a "message" fact per Message, "run_start" and "run_end"
// log Entries, an "events" log Entry with a copy of the Events, and so on. A
// Store hands them back as they are, and a Record is rebuilt from them as it
// always was; new Entries are Events, appended after them. The kinds never
// change, so these constants are the whole of the legacy format.
const (
	legacyKindCheckpoint = "checkpoint"
	legacyKindMessage    = "message"
	legacyKindMetadata   = "metadata"
	legacyKindClaim      = "claim"
	legacyKindTools      = "tools_started"
	legacyKindSnapshot   = "log_snapshot"
	legacyKindRunStart   = "run_start"
	legacyKindRunEnd     = "run_end"
	legacyKindEvents     = "events"
	legacyKindModelCall  = "model_call"
	legacyKindReplyPart  = "reply_part"
	legacyKindRelease    = "release"
	legacyKindBusy       = "busy"
)

var legacyFacts = map[EntryKind]bool{
	legacyKindCheckpoint: true, legacyKindMessage: true, legacyKindMetadata: true,
	legacyKindClaim: true, legacyKindTools: true,
}

var legacyLog = map[EntryKind]bool{
	legacyKindSnapshot: true, legacyKindRunStart: true, legacyKindRunEnd: true, legacyKindEvents: true,
	legacyKindModelCall: true, legacyKindReplyPart: true, legacyKindRelease: true, legacyKindBusy: true,
}

func isLegacy(kind EntryKind) bool { return legacyFacts[kind] || legacyLog[kind] }

// legacyCheckpointData is what a "checkpoint" Entry held.
type legacyCheckpointData struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id,omitempty"`
	ParentSeq   uint64            `json:"parent_seq,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Messages    []gotato.Message  `json:"messages"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Compactions []Compaction      `json:"compactions,omitempty"`
	EventLimit  int               `json:"event_limit,omitempty"`
	Claim       Claim             `json:"claim"`
}

type legacyToolsStarted struct {
	MessageID gotato.MessageID    `json:"message_id"`
	Calls     []gotato.ToolCallID `json:"calls"`
}

type legacyReplyPart struct {
	RunID     gotato.RunID      `json:"run_id"`
	Turn      gotato.TurnNumber `json:"turn"`
	MessageID gotato.MessageID  `json:"message_id"`
	Text      string            `json:"text,omitempty"`
	Reasoning string            `json:"reasoning,omitempty"`
}

type legacyMetadataChange struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

type legacySnapshot struct {
	Runs  []Run        `json:"runs,omitempty"`
	Usage gotato.Usage `json:"usage"`
}

// applyLegacy folds one legacy Entry into the Record's state. The caller holds
// the lock.
func (s *Record) applyLegacy(entry Entry) error {
	decode := func(v any) error {
		if err := json.Unmarshal(entry.Data, v); err != nil {
			return gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: entry %d (%s): %v", entry.Seq, entry.Kind, err))
		}
		return nil
	}
	switch entry.Kind {
	case legacyKindCheckpoint:
		var checkpoint legacyCheckpointData
		if err := decode(&checkpoint); err != nil {
			return err
		}
		s.id = checkpoint.ID
		s.parentID = checkpoint.ParentID
		s.parentSeq = checkpoint.ParentSeq
		s.createdAt = checkpoint.CreatedAt
		s.messages = checkpoint.Messages
		s.metadata = checkpoint.Metadata
		if s.metadata == nil {
			s.metadata = map[string]string{}
		}
		s.compactions = checkpoint.Compactions
		s.claim = checkpoint.Claim
		s.started = nil
		s.openParts = nil
		s.eventLimit = checkpoint.EventLimit
		if s.eventLimit == 0 {
			s.eventLimit = DefaultEventLimit
		}
	case legacyKindMessage:
		var message gotato.Message
		if err := decode(&message); err != nil {
			return err
		}
		s.closeParts(message.ID)
		s.messages = append(s.messages, message)
	case legacyKindTools:
		var started legacyToolsStarted
		if err := decode(&started); err != nil {
			return err
		}
		if s.started == nil {
			s.started = map[gotato.ToolCallID]bool{}
		}
		for _, call := range started.Calls {
			s.started[call] = true
		}
	case legacyKindReplyPart:
		var part legacyReplyPart
		if err := decode(&part); err != nil {
			return err
		}
		s.addPart(replyPart{RunID: part.RunID, MessageID: part.MessageID, Text: part.Text, Reasoning: part.Reasoning})
	case legacyKindMetadata:
		var change legacyMetadataChange
		if err := decode(&change); err != nil {
			return err
		}
		if change.Value == "" {
			delete(s.metadata, change.Key)
		} else {
			s.metadata[change.Key] = change.Value
		}
	case legacyKindClaim:
		var claim Claim
		if err := decode(&claim); err != nil {
			return err
		}
		s.claim = claim
	case legacyKindSnapshot:
		var snapshot legacySnapshot
		if err := decode(&snapshot); err != nil {
			return err
		}
		s.runs = snapshot.Runs
		s.usage = snapshot.Usage
		s.running = ""
		if n := len(s.runs); n > 0 && s.runs[n-1].Status == gotato.RunRunning {
			s.running = s.runs[n-1].RunID
		}
	case legacyKindRunStart:
		var run Run
		if err := decode(&run); err != nil {
			return err
		}
		s.runs = append(s.runs, run)
		s.running = run.RunID
	case legacyKindRunEnd:
		var run Run
		if err := decode(&run); err != nil {
			return err
		}
		s.endRun(run)
		s.dropParts(run.RunID)
	case legacyKindModelCall, legacyKindEvents, legacyKindRelease, legacyKindBusy:
		// Nothing to rebuild: the Events they held are not read again.
	}
	return nil
}

// legacyEventOf maps a legacy Entry onto the Event that now says the same
// thing. Where one Entry held several things, as a tools_started naming
// several calls does, the Event names the first. A log snapshot and an
// "events" copy of Events have no Event.
func legacyEventOf(entry Entry, sessionID gotato.SessionID) (gotato.Event, bool, error) {
	header := gotato.Header{Time: entry.At, SessionID: sessionID, Number: entry.Seq}
	decode := func(v any) error {
		if err := json.Unmarshal(entry.Data, v); err != nil {
			return fmt.Errorf("session: entry %d (%s): %w", entry.Seq, entry.Kind, err)
		}
		return nil
	}
	switch entry.Kind {
	case legacyKindCheckpoint:
		var checkpoint legacyCheckpointData
		if err := decode(&checkpoint); err != nil {
			return nil, false, err
		}
		if entry.Seq == 1 {
			created := gotato.SessionCreated{Header: header, CreatedAt: checkpoint.CreatedAt, Messages: checkpoint.Messages, Metadata: checkpoint.Metadata, Compactions: checkpoint.Compactions}
			if checkpoint.ParentID != "" {
				created.ForkedFrom = &gotato.ForkedFrom{SessionID: gotato.SessionID(checkpoint.ParentID), Number: checkpoint.ParentSeq}
			}
			return created, true, nil
		}
		ended := gotato.CompactionEnded{Header: header, Why: gotato.CompactionOnDemand, History: checkpoint.Compactions, Messages: checkpoint.Messages, Metadata: checkpoint.Metadata}
		if n := len(checkpoint.Compactions); n > 0 {
			last := checkpoint.Compactions[n-1]
			ended.Compaction = &last
		}
		return ended, true, nil
	case legacyKindMessage:
		var message gotato.Message
		if err := decode(&message); err != nil {
			return nil, false, err
		}
		switch {
		case message.Role == gotato.RoleUser:
			return gotato.MessageCommitted{Header: header, Message: message, Source: gotato.SourcePrompt}, true, nil
		case message.Role == gotato.RoleToolResult && message.ToolResult != nil:
			return gotato.ToolResultCommitted{Header: header, Message: message, CallID: message.ToolResult.CallID, Status: message.ToolResult.Status}, true, nil
		case message.StopReason == gotato.StopCutOff:
			return gotato.ReplyCutOff{Header: header, Message: message}, true, nil
		default:
			return gotato.ReplyEnded{Header: header, Message: message, Model: message.Model, StopReason: message.StopReason, ToolCalls: len(message.ToolCalls)}, true, nil
		}
	case legacyKindMetadata:
		var change legacyMetadataChange
		if err := decode(&change); err != nil {
			return nil, false, err
		}
		return gotato.MetadataChanged{Header: header, Key: change.Key, Value: change.Value}, true, nil
	case legacyKindClaim, legacyKindBusy:
		var claim Claim
		if err := decode(&claim); err != nil {
			return nil, false, err
		}
		return gotato.SessionClaimed{Header: header, Instance: claim.Instance, Epoch: claim.Epoch, Why: gotato.ClaimIdle}, true, nil
	case legacyKindRelease:
		var claim Claim
		if err := decode(&claim); err != nil {
			return nil, false, err
		}
		return gotato.SessionReleased{Header: header, Instance: claim.Instance, Epoch: claim.Epoch}, true, nil
	case legacyKindTools:
		var started legacyToolsStarted
		if err := decode(&started); err != nil {
			return nil, false, err
		}
		if len(started.Calls) == 0 {
			return nil, false, nil
		}
		return gotato.ToolStarted{Header: header, MessageID: started.MessageID, CallID: started.Calls[0]}, true, nil
	case legacyKindRunStart:
		var run Run
		if err := decode(&run); err != nil {
			return nil, false, err
		}
		header.RunID, header.Time = run.RunID, run.StartedAt
		return gotato.RunStarted{Header: header}, true, nil
	case legacyKindRunEnd:
		var run Run
		if err := decode(&run); err != nil {
			return nil, false, err
		}
		header.RunID, header.Time = run.RunID, run.EndedAt
		return gotato.RunEnded{Header: header, Status: run.Status, Cause: gotato.ErrorCode(run.Error), StopReason: run.StopReason, Turns: run.Turns, ToolCalls: run.ToolCalls, Usage: run.Usage}, true, nil
	case legacyKindModelCall:
		var call struct {
			RunID gotato.RunID      `json:"run_id"`
			Turn  gotato.TurnNumber `json:"turn"`
		}
		if err := decode(&call); err != nil {
			return nil, false, err
		}
		header.RunID, header.Turn = call.RunID, call.Turn
		return gotato.ReplyStarted{Header: header}, true, nil
	case legacyKindReplyPart:
		var part legacyReplyPart
		if err := decode(&part); err != nil {
			return nil, false, err
		}
		header.RunID, header.Turn = part.RunID, part.Turn
		return gotato.ReplyPartKept{Header: header, MessageID: part.MessageID, Text: part.Text, Reasoning: part.Reasoning}, true, nil
	}
	return nil, false, nil
}

// legacySnapshotEntry holds a document's Runs and usage, which it kept
// beside its Messages.
func legacySnapshotEntry(seq uint64, record *Record) (Entry, error) {
	record.mu.RLock()
	defer record.mu.RUnlock()
	data, err := json.Marshal(legacySnapshot{Runs: record.runs, Usage: record.usage})
	if err != nil {
		return Entry{}, err
	}
	return Entry{Seq: seq, Kind: legacyKindSnapshot, Layer: LayerLog, At: record.updatedAt, Data: data}, nil
}
