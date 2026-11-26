package record

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// startLog turns the Record's log on, with the Record's current state as its
// first Event, session_created, a Checkpoint. A Record keeps no pending
// Events until then, so one that is never stored does not grow.
func (s *Record) startLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logging = true
	s.pending = nil
	s.events, s.eventHead = nil, 0
	s.last = 0
	if s.epoch == "" {
		s.epoch = newEpoch()
	}
	// The Segments the Context starts with are stored once, each as its own
	// Entry after this Checkpoint, so session_created holds the tail alone.
	tail := cloneMessages(s.messages[min(s.prefixLen(), len(s.messages)):])
	created := gotato.SessionCreated{
		Header:      s.header(),
		CreatedAt:   s.createdAt,
		RecordEpoch: s.epoch,
		Messages:    tail,
		Metadata:    maps.Clone(s.metadata),
		Compactions: slices.Clone(s.compactions),
		LoadedTools: slices.Clone(s.loaded),
		Segments:    slices.Clone(s.used),
		Tail:        tailPositions(tail, s.positions),
		Runs:        s.baseRuns + len(s.runs),
		Usage:       s.usage,
		LastRunEnd:  s.lastRunEnd,
	}
	created.Time = s.createdAt
	if s.parentID != "" {
		created.ForkedFrom = &gotato.ForkedFrom{SessionID: gotato.SessionID(s.parentID), Number: s.parentSeq}
	}
	s.number(created)
	for _, number := range s.used {
		s.number(gotato.SegmentClosed{Header: s.header(), Segment: s.closed[number].Clone()})
	}
}

// TakePending returns the Entries of the pending Events, numbered from
// head+1, the Events themselves, and the Header the store keeps once they are
// written, and clears them. The Record's head moves only when Stored confirms
// the write.
func (s *Record) TakePending() ([]Entry, []gotato.Event, Header, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil, nil, Header{}, nil
	}
	events := s.pending
	entries := make([]Entry, len(events))
	for i, event := range events {
		entry, err := entryOf(event, s.instance)
		if err != nil {
			return nil, nil, Header{}, err
		}
		if entry.Seq != s.head+uint64(i)+1 {
			return nil, nil, Header{}, gotato.ErrorOf(gotato.CodeInternalInvariant, fmt.Sprintf("record: Event %d queued at position %d", entry.Seq, s.head+uint64(i)+1))
		}
		entries[i] = entry
	}
	s.pending = nil
	return entries, events, Header{Epoch: s.epoch, Info: s.infoLocked()}, nil
}

// Stored records that the store now holds every Entry up to seq.
func (s *Record) Stored(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = max(s.head, seq)
	s.spilled.trim(s.head, defaultSpillCacheBytes)
}

// Head returns the Seq of the last Entry the store holds for this Record, or
// 0 when the Record has never been stored.
func (s *Record) Head() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.head
}

// checkpoint replaces the committed Messages with messages, as a Compaction
// does, records compaction when it is not nil, and appends a compaction_ended
// Event, a Checkpoint, to the log. The Context is then messages alone, with no
// Segments (a compaction that closes a Segment writes a Checkpoint that lists
// it instead).
// Nothing written before is rewritten: the Messages it replaces stay in the
// store, and a load starts after them. It must not run while a Run is using
// the Record.
func (s *Record) checkpoint(why gotato.CompactionWhy, messages []gotato.Message, compaction *gotato.Compaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneMessages(messages)
	s.used = nil
	s.checkpointLocked(why, messages, compaction)
}

// fromEntries rebuilds a Record from its log, starting at a Checkpoint: the
// facts a Store loads, or the whole log. The Entries must be in Seq order. The
// facts alone rebuild the whole Record, and log Entries, when present, add
// only Events to its window. An Entry of an unknown kind is refused rather
// than misread.
func fromEntries(entries []Entry) (*Record, error) {
	return rebuildRecord(entries, true, nil)
}

// rebuildRecord rebuilds a Record from entries, which start at a Checkpoint.
// segments are the segment_closed Entries from before that Checkpoint, which
// it may list.
func rebuildRecord(entries []Entry, retain bool, segments []Entry) (*Record, error) {
	if len(entries) == 0 || !entries[0].IsCheckpoint() {
		return nil, gotato.ErrorOf(gotato.CodeInvalidArgument, "record: a log must start at a checkpoint")
	}
	s := &Record{eventLimit: defaultEventLimit, now: time.Now, metadata: map[string]string{}, retainRead: retain, closed: map[int]gotato.Segment{}, positions: map[gotato.MessageID]gotato.MessagePosition{}}
	if err := applySegments(s, segments); err != nil {
		return nil, err
	}
	if err := applyLog(s, entries); err != nil {
		return nil, err
	}
	if s.id == "" {
		return nil, gotato.ErrorOf(gotato.CodeInvalidArgument, "record: checkpoint has no id")
	}
	if err := s.resolveSegments(); err != nil {
		return nil, err
	}
	before := len(s.messages)
	s.closeParts("")
	s.cutOff = slices.Clone(s.messages[before:])
	s.messages = repair(s.messages, s.started)
	s.started = nil
	s.head = entries[len(entries)-1].Seq
	s.last = s.head
	s.logging = true
	return s, nil
}

// applySegments folds the segment_closed Entries from before a Checkpoint
// into s.
func applySegments(s *Record, segments []Entry) error {
	for _, entry := range segments {
		if entry.Kind != gotato.EventKindSegmentClosed {
			return gotato.ErrorOf(gotato.CodeInvalidArgument, fmt.Sprintf("record: entry %d (%s) is not a segment_closed Entry", entry.Seq, entry.Kind))
		}
		if err := s.apply(entry); err != nil {
			return err
		}
	}
	return nil
}

// applyLog folds entries, in Seq order, into s.
func applyLog(s *Record, entries []Entry) error {
	for i, entry := range entries {
		if i > 0 && entry.Seq <= entries[i-1].Seq {
			return gotato.ErrorOf(gotato.CodeInvalidArgument, fmt.Sprintf("record: entry %d follows entry %d", entry.Seq, entries[i-1].Seq))
		}
		if err := s.apply(entry); err != nil {
			return err
		}
		s.updatedAt = entry.At
	}
	return nil
}

// apply folds one Entry into the Record's state, as a rebuild does. The
// caller has the Record to itself.
func (s *Record) apply(entry Entry) error {
	event, err := gotato.UnmarshalEvent(entry.Data)
	if err != nil {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, fmt.Sprintf("record: entry %d (%s): %v", entry.Seq, entry.Kind, err))
	}
	if _, unknown := event.(gotato.UnknownEvent); unknown {
		return gotato.ErrorOf(gotato.CodeNotSupported, fmt.Sprintf("record: entry %d has unknown kind %q", entry.Seq, entry.Kind))
	}
	event = gotato.WithNumber(event, entry.Seq)
	if !s.foldCheckpoint(event) && !s.foldMessage(event) && !s.foldRun(event) {
		s.foldSession(event)
	}
	at := event.Head().Time
	if at.IsZero() {
		at = entry.At
	}
	s.indexMessage(event, at)
	if s.retainRead {
		s.retain(gotato.WithoutBody(event))
	}
	return nil
}

// foldCheckpoint folds a Checkpoint Event into the Record's state, and says
// whether event was one.
func (s *Record) foldCheckpoint(event gotato.Event) bool {
	switch e := event.(type) {
	case gotato.SessionCreated:
		s.id = string(e.SessionID)
		s.createdAt = e.CreatedAt
		s.epoch = e.RecordEpoch
		s.parentID, s.parentSeq = "", 0
		if e.ForkedFrom != nil {
			s.parentID, s.parentSeq = string(e.ForkedFrom.SessionID), e.ForkedFrom.Number
		}
		s.restore(checkpointState{messages: e.Messages, metadata: e.Metadata, compactions: e.Compactions, loaded: e.LoadedTools, segments: e.Segments, tail: e.Tail})
		s.restoreTotals(e.Runs, e.Usage, e.LastRunEnd)
	case gotato.CompactionEnded:
		if s.id == "" {
			// A load that starts at this Checkpoint has not read session_created.
			s.id = string(e.SessionID)
		}
		s.restore(checkpointState{messages: e.Messages, metadata: e.Metadata, compactions: e.History, loaded: e.LoadedTools, segments: e.Segments, tail: e.Tail})
		s.restoreTotals(e.Runs, e.Usage, e.LastRunEnd)
		s.notices = slices.Clone(e.Notices)
		s.running = e.Running
		if e.Instance != "" {
			// The writer that holds the Session writes its Checkpoint, so the
			// Session is busy until a session_released after it says otherwise.
			s.claim = Claim{Instance: e.Instance, Epoch: e.Epoch}
			s.busy = true
		}
	default:
		return false
	}
	return true
}

// foldMessage folds an Event that commits or queues a Message, or keeps a
// part of a reply, into the Record's state, and says whether event was one.
func (s *Record) foldMessage(event gotato.Event) bool {
	switch e := event.(type) {
	case gotato.MessageQueued:
		s.foldQueued(e)
	case gotato.MessageCommitted:
		s.addMessage(e.Message)
		s.foldQueued(e)
	case gotato.NoticeCommitted:
		s.addMessage(e.Message)
		s.foldNotice(e)
	case gotato.NoticeQueued, gotato.MessageDropped:
		s.foldNotice(e)
		s.foldQueued(e)
	case gotato.ReplyEnded:
		s.addMessage(e.Message)
	case gotato.ReplyCutOff:
		s.addMessage(e.Message)
	case gotato.ToolResultCommitted:
		s.addMessage(e.Message)
	case gotato.ReplyPartKept:
		s.addPart(replyPart{RunID: e.RunID, MessageID: e.MessageID, Text: e.Text, Reasoning: e.Reasoning})
	default:
		return false
	}
	return true
}

// foldRun folds an Event of a Run or of its Tools into the Record's state, and
// says whether event was one.
func (s *Record) foldRun(event gotato.Event) bool {
	switch e := event.(type) {
	case gotato.ToolStarted:
		if s.started == nil {
			s.started = map[gotato.ToolCallID]bool{}
		}
		s.started[e.CallID] = true
	case gotato.ToolLoaded:
		s.load(e.ToolID)
	case gotato.ToolUnloaded:
		s.unload(e.ToolID)
	case gotato.RunStarted:
		s.runs = append(s.runs, runStarted(e))
		s.running = e.RunID
	case gotato.RunEnded:
		s.endRun(runEnded(e))
		// A reply of a Run that ended and left parts but no Message was
		// refused, by a limit for one: it is not part of the history.
		s.dropParts(e.RunID)
	default:
		return false
	}
	return true
}

// foldSession folds an Event of the Session itself, its Segments, Metadata or
// Claim, into the Record's state.
func (s *Record) foldSession(event gotato.Event) {
	switch e := event.(type) {
	case gotato.SegmentClosed:
		s.closeLocked(e.Segment)
	case gotato.MetadataChanged:
		if e.Value == "" {
			delete(s.metadata, e.Key)
		} else {
			s.metadata[e.Key] = e.Value
		}
	case gotato.SessionClaimed:
		s.claim = Claim{Instance: e.Instance, Epoch: e.Epoch}
		s.busy = true
	case gotato.SessionReleased:
		s.busy = false
	}
}

// checkpointState is the state a Checkpoint (session_created or
// compaction_ended) holds that restore sets.
type checkpointState struct {
	messages    []gotato.Message
	metadata    map[string]string
	compactions []gotato.Compaction
	loaded      []string
	segments    []int
	tail        []gotato.MessagePosition
}

// restore sets the state a Checkpoint (session_created or compaction_ended)
// holds, and forgets what was in flight before it.
func (s *Record) restore(state checkpointState) {
	s.messages = state.messages
	s.metadata = state.metadata
	if s.metadata == nil {
		s.metadata = map[string]string{}
	}
	s.compactions = state.compactions
	s.loaded = slices.Clone(state.loaded)
	s.startContext(state.segments, state.tail)
	s.started = nil
	s.openParts = nil
}

// restoreTotals sets the totals a Checkpoint holds. The Runs it counts that
// the rebuild has not read are counted in baseRuns: all of them for a load that
// starts at the Checkpoint, none for a rebuild of the whole log.
func (s *Record) restoreTotals(runs int, usage gotato.Usage, lastRunEnd time.Time) {
	s.baseRuns = runs - len(s.runs)
	s.usage = usage
	s.lastRunEnd = lastRunEnd.UTC()
}

// startContext begins the Context a Checkpoint holds: the Segments it lists,
// whose Messages resolveSegments puts in front once the load has read them,
// and the positions of the Messages it keeps.
func (s *Record) startContext(segments []int, tail []gotato.MessagePosition) {
	s.used = slices.Clone(segments)
	s.positions = map[gotato.MessageID]gotato.MessagePosition{}
	for _, position := range tail {
		s.positions[position.ID] = position
	}
}

func (s *Record) addMessage(message gotato.Message) {
	s.closeParts(message.ID)
	s.messages = append(s.messages, message)
}

// addPart collects a part of a reply that was streaming. The caller holds the
// lock.
func (s *Record) addPart(part replyPart) {
	for i := range s.openParts {
		if s.openParts[i].MessageID == part.MessageID {
			s.openParts[i].Text += part.Text
			s.openParts[i].Reasoning += part.Reasoning
			return
		}
	}
	s.openParts = append(s.openParts, part)
}

// dropParts forgets the parts of the replies of a Run that ended. The caller
// holds the lock.
func (s *Record) dropParts(runID gotato.RunID) {
	s.openParts = slices.DeleteFunc(s.openParts, func(part replyPart) bool { return part.RunID == runID })
}

// closeParts ends the streaming replies whose Message never arrived, but the
// one with ID committed, as cut-off replies with the text they streamed. The
// caller holds the lock.
func (s *Record) closeParts(committed gotato.MessageID) {
	for _, part := range s.openParts {
		if part.MessageID == committed || part.Text == "" {
			continue
		}
		s.messages = append(s.messages, gotato.Message{
			ID: part.MessageID, RunID: part.RunID, Role: gotato.RoleAssistant, StopReason: gotato.StopReasonCutOff,
			Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: part.Text}},
		})
	}
	s.openParts = nil
}

// Results that repair adds for Tool calls left without one.
const (
	// StoppedToolResult answers a call that started and never finished.
	StoppedToolResult = "The instance running this tool stopped before the tool finished. Whether the tool took effect is unknown."
	// notRunToolResult answers a call that never started.
	notRunToolResult = "This tool call was not run: the instance stopped before it started."
)

// repair answers, in memory, every Tool call that has no result. A Run whose
// writer stopped leaves such calls behind, and a provider refuses a request
// with an unanswered call. A call that started is reported as stopped with an
// unknown outcome, and one that never started as not run. Nothing is written
// for them: every load works them out the same way, with the same IDs.
func repair(messages []gotato.Message, started map[gotato.ToolCallID]bool) []gotato.Message {
	var out []gotato.Message
	for i := 0; i < len(messages); {
		message := messages[i]
		if message.Role != gotato.RoleAssistant || len(message.ToolCalls) == 0 {
			if out != nil {
				out = append(out, message)
			}
			i++
			continue
		}
		out, i = repairCalls(out, messages, i, started)
	}
	if out == nil {
		return messages
	}
	return out
}

// repairCalls answers the calls of messages[i] that have no result: it appends
// messages[i], the Tool Results right after it and a result for each missing
// call to out, and returns out and the position to go on from. out stays nil
// while nothing needed an answer.
func repairCalls(out, messages []gotato.Message, i int, started map[gotato.ToolCallID]bool) ([]gotato.Message, int) {
	missing, end := missingResults(messages, i)
	if len(missing) == 0 && out == nil {
		return nil, i + 1
	}
	if out == nil {
		out = append(make([]gotato.Message, 0, len(messages)+len(missing)), messages[:i]...)
	}
	out = append(out, messages[i:end]...)
	for _, call := range missing {
		if started[call.ID] {
			out = append(out, stoppedResult(call))
		} else {
			out = append(out, notRunResult(call))
		}
	}
	return out, end
}

// missingResults returns the calls of messages[i] that the Tool Results right
// after it do not answer, and the position past those results.
func missingResults(messages []gotato.Message, i int) (missing []gotato.ToolCall, end int) {
	answered := map[gotato.ToolCallID]bool{}
	end = i + 1
	for ; end < len(messages) && messages[end].Role == gotato.RoleToolResult; end++ {
		if result := messages[end].ToolResult; result != nil {
			answered[result.CallID] = true
		}
	}
	for _, call := range messages[i].ToolCalls {
		if !answered[call.ID] {
			missing = append(missing, call)
		}
	}
	return missing, end
}

// stoppedResult is the failed result repair adds for a call that had started.
// Its ID comes from the call's, so every load works out the same Message.
func stoppedResult(call gotato.ToolCall) gotato.Message {
	return repairResultWith(call, true, StoppedToolResult)
}

// notRunResult is the failed result repair adds for a call that had not
// started, with an ID that comes from the call's, as stoppedResult's does.
func notRunResult(call gotato.ToolCall) gotato.Message {
	return repairResultWith(call, false, notRunToolResult)
}

// repairResultWith is the failed result repair adds for a call, with the
// text given; started says whether the call had started.
func repairResultWith(call gotato.ToolCall, started bool, text string) gotato.Message {
	return gotato.Message{
		ID:    gotato.MessageID("repair-" + string(call.ID)),
		Role:  gotato.RoleToolResult,
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: text}},
		ToolResult: &gotato.ToolResult{
			CallID: call.ID, Status: gotato.ToolResultStatusFailed, Executed: started, SafeError: text,
			Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: text}},
		},
	}
}
