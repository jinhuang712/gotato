package gotato

// The kind, the class and the JSON codec of every Event type. Each type states
// its own class, so a type of another package states its own too. The fields of each
// type are in events.go.

// The kinds of Event, as they are named in JSON.
const (
	EventKindRunStarted          EventKind = "run_started"
	EventKindRunEnded            EventKind = "run_ended"
	EventKindTurnStarted         EventKind = "turn_started"
	EventKindTurnEnded           EventKind = "turn_ended"
	EventKindContextBuilt        EventKind = "context_built"
	EventKindReplyStarted        EventKind = "reply_started"
	EventKindReplyEnded          EventKind = "reply_ended"
	EventKindTextDelta           EventKind = "text_delta"
	EventKindReasoningDelta      EventKind = "reasoning_delta"
	EventKindReplyCutOff         EventKind = "reply_cut_off"
	EventKindReplyPartKept       EventKind = "reply_part_kept"
	EventKindModelRetried        EventKind = "model_retried"
	EventKindToolStarted         EventKind = "tool_started"
	EventKindToolEnded           EventKind = "tool_ended"
	EventKindToolProgress        EventKind = "tool_progress"
	EventKindToolResultCommitted EventKind = "tool_result_committed"
	EventKindToolResultSpilled   EventKind = "tool_result_spilled"
	EventKindToolLoaded          EventKind = "tool_loaded"
	EventKindToolUnloaded        EventKind = "tool_unloaded"
	EventKindRunRequested        EventKind = "run_requested"
	EventKindRunWaiting          EventKind = "run_waiting"
	EventKindRunRejected         EventKind = "run_rejected"
	EventKindMessageQueued       EventKind = "message_queued"
	EventKindMessageCommitted    EventKind = "message_committed"
	EventKindMessageDropped      EventKind = "message_dropped"
	EventKindNoticeQueued        EventKind = "notice_queued"
	EventKindNoticeCommitted     EventKind = "notice_committed"
	EventKindMessageRejected     EventKind = "message_rejected"
	EventKindAbortRequested      EventKind = "abort_requested"
	EventKindSessionClaimed      EventKind = "session_claimed"
	EventKindSessionReleased     EventKind = "session_released"
	EventKindSessionFenced       EventKind = "session_fenced"
	EventKindLivenessLapsed      EventKind = "liveness_lapsed"
	EventKindSessionCreated      EventKind = "session_created"
	EventKindMetadataChanged     EventKind = "metadata_changed"
	EventKindCompactionStarted   EventKind = "compaction_started"
	EventKindCompactionEnded     EventKind = "compaction_ended"
	EventKindCompactionFailed    EventKind = "compaction_failed"
	EventKindSegmentClosed       EventKind = "segment_closed"
	EventKindSessionUnloaded     EventKind = "session_unloaded"
	EventKindSessionDeleted      EventKind = "session_deleted"
	EventKindSessionLoaded       EventKind = "session_loaded"
	EventKindSessionSaved        EventKind = "session_saved"
	EventKindStoreWriteFailed    EventKind = "store_write_failed"
	EventKindExtensionFailed     EventKind = "extension_failed"
	EventKindEventsSkipped       EventKind = "events_skipped"
)

// Kind implements Event.
func (RunStarted) Kind() EventKind { return EventKindRunStarted }

// Class implements Event.
func (RunStarted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunStarted) MarshalJSON() ([]byte, error) {
	type plain RunStarted
	return marshalKind(EventKindRunStarted, plain(e))
}

// Kind implements Event.
func (RunEnded) Kind() EventKind { return EventKindRunEnded }

// Class implements Event.
func (RunEnded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunEnded) MarshalJSON() ([]byte, error) {
	type plain RunEnded
	return marshalKind(EventKindRunEnded, plain(e))
}

// Kind implements Event.
func (TurnStarted) Kind() EventKind { return EventKindTurnStarted }

// Class implements Event.
func (TurnStarted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TurnStarted) MarshalJSON() ([]byte, error) {
	type plain TurnStarted
	return marshalKind(EventKindTurnStarted, plain(e))
}

// Kind implements Event.
func (TurnEnded) Kind() EventKind { return EventKindTurnEnded }

// Class implements Event.
func (TurnEnded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TurnEnded) MarshalJSON() ([]byte, error) {
	type plain TurnEnded
	return marshalKind(EventKindTurnEnded, plain(e))
}

// Kind implements Event.
func (ContextBuilt) Kind() EventKind { return EventKindContextBuilt }

// Class implements Event.
func (ContextBuilt) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ContextBuilt) MarshalJSON() ([]byte, error) {
	type plain ContextBuilt
	return marshalKind(EventKindContextBuilt, plain(e))
}

// Kind implements Event.
func (ReplyStarted) Kind() EventKind { return EventKindReplyStarted }

// Class implements Event.
func (ReplyStarted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyStarted) MarshalJSON() ([]byte, error) {
	type plain ReplyStarted
	return marshalKind(EventKindReplyStarted, plain(e))
}

// Kind implements Event.
func (ReplyEnded) Kind() EventKind { return EventKindReplyEnded }

// Class implements Event.
func (ReplyEnded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyEnded) MarshalJSON() ([]byte, error) {
	type plain ReplyEnded
	return marshalKind(EventKindReplyEnded, plain(e))
}

// Kind implements Event.
func (TextDelta) Kind() EventKind { return EventKindTextDelta }

// Class implements Event.
func (TextDelta) Class() EventClass { return EventClassDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TextDelta) MarshalJSON() ([]byte, error) {
	type plain TextDelta
	return marshalKind(EventKindTextDelta, plain(e))
}

// Kind implements Event.
func (ReasoningDelta) Kind() EventKind { return EventKindReasoningDelta }

// Class implements Event.
func (ReasoningDelta) Class() EventClass { return EventClassDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReasoningDelta) MarshalJSON() ([]byte, error) {
	type plain ReasoningDelta
	return marshalKind(EventKindReasoningDelta, plain(e))
}

// Kind implements Event.
func (ReplyCutOff) Kind() EventKind { return EventKindReplyCutOff }

// Class implements Event.
func (ReplyCutOff) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyCutOff) MarshalJSON() ([]byte, error) {
	type plain ReplyCutOff
	return marshalKind(EventKindReplyCutOff, plain(e))
}

// Kind implements Event.
func (ReplyPartKept) Kind() EventKind { return EventKindReplyPartKept }

// Class implements Event.
func (ReplyPartKept) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyPartKept) MarshalJSON() ([]byte, error) {
	type plain ReplyPartKept
	return marshalKind(EventKindReplyPartKept, plain(e))
}

// Kind implements Event.
func (ModelRetried) Kind() EventKind { return EventKindModelRetried }

// Class implements Event.
func (ModelRetried) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ModelRetried) MarshalJSON() ([]byte, error) {
	type plain ModelRetried
	return marshalKind(EventKindModelRetried, plain(e))
}

// Kind implements Event.
func (ToolStarted) Kind() EventKind { return EventKindToolStarted }

// Class implements Event.
func (ToolStarted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolStarted) MarshalJSON() ([]byte, error) {
	type plain ToolStarted
	return marshalKind(EventKindToolStarted, plain(e))
}

// Kind implements Event.
func (ToolEnded) Kind() EventKind { return EventKindToolEnded }

// Class implements Event.
func (ToolEnded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolEnded) MarshalJSON() ([]byte, error) {
	type plain ToolEnded
	return marshalKind(EventKindToolEnded, plain(e))
}

// Kind implements Event.
func (ToolProgress) Kind() EventKind { return EventKindToolProgress }

// Class implements Event.
func (ToolProgress) Class() EventClass { return EventClassDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolProgress) MarshalJSON() ([]byte, error) {
	type plain ToolProgress
	return marshalKind(EventKindToolProgress, plain(e))
}

// Kind implements Event.
func (ToolResultCommitted) Kind() EventKind { return EventKindToolResultCommitted }

// Class implements Event.
func (ToolResultCommitted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolResultCommitted) MarshalJSON() ([]byte, error) {
	type plain ToolResultCommitted
	return marshalKind(EventKindToolResultCommitted, plain(e))
}

// Kind implements Event.
func (ToolResultSpilled) Kind() EventKind { return EventKindToolResultSpilled }

// Class implements Event.
func (ToolResultSpilled) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolResultSpilled) MarshalJSON() ([]byte, error) {
	type plain ToolResultSpilled
	return marshalKind(EventKindToolResultSpilled, plain(e))
}

// Kind implements Event.
func (ToolLoaded) Kind() EventKind { return EventKindToolLoaded }

// Class implements Event.
func (ToolLoaded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolLoaded) MarshalJSON() ([]byte, error) {
	type plain ToolLoaded
	return marshalKind(EventKindToolLoaded, plain(e))
}

// Kind implements Event.
func (ToolUnloaded) Kind() EventKind { return EventKindToolUnloaded }

// Class implements Event.
func (ToolUnloaded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolUnloaded) MarshalJSON() ([]byte, error) {
	type plain ToolUnloaded
	return marshalKind(EventKindToolUnloaded, plain(e))
}

// Kind implements Event.
func (RunRequested) Kind() EventKind { return EventKindRunRequested }

// Class implements Event.
func (RunRequested) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunRequested) MarshalJSON() ([]byte, error) {
	type plain RunRequested
	return marshalKind(EventKindRunRequested, plain(e))
}

// Kind implements Event.
func (RunWaiting) Kind() EventKind { return EventKindRunWaiting }

// Class implements Event.
func (RunWaiting) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunWaiting) MarshalJSON() ([]byte, error) {
	type plain RunWaiting
	return marshalKind(EventKindRunWaiting, plain(e))
}

// Kind implements Event.
func (RunRejected) Kind() EventKind { return EventKindRunRejected }

// Class implements Event.
func (RunRejected) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunRejected) MarshalJSON() ([]byte, error) {
	type plain RunRejected
	return marshalKind(EventKindRunRejected, plain(e))
}

// Kind implements Event.
func (MessageQueued) Kind() EventKind { return EventKindMessageQueued }

// Class implements Event.
func (MessageQueued) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageQueued) MarshalJSON() ([]byte, error) {
	type plain MessageQueued
	return marshalKind(EventKindMessageQueued, plain(e))
}

// Kind implements Event.
func (MessageCommitted) Kind() EventKind { return EventKindMessageCommitted }

// Class implements Event.
func (MessageCommitted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageCommitted) MarshalJSON() ([]byte, error) {
	type plain MessageCommitted
	return marshalKind(EventKindMessageCommitted, plain(e))
}

// Kind implements Event.
func (MessageDropped) Kind() EventKind { return EventKindMessageDropped }

// Class implements Event.
func (MessageDropped) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageDropped) MarshalJSON() ([]byte, error) {
	type plain MessageDropped
	return marshalKind(EventKindMessageDropped, plain(e))
}

// Kind implements Event.
func (NoticeQueued) Kind() EventKind { return EventKindNoticeQueued }

// Class implements Event.
func (NoticeQueued) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e NoticeQueued) MarshalJSON() ([]byte, error) {
	type plain NoticeQueued
	return marshalKind(EventKindNoticeQueued, plain(e))
}

// Kind implements Event.
func (NoticeCommitted) Kind() EventKind { return EventKindNoticeCommitted }

// Class implements Event.
func (NoticeCommitted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e NoticeCommitted) MarshalJSON() ([]byte, error) {
	type plain NoticeCommitted
	return marshalKind(EventKindNoticeCommitted, plain(e))
}

// Kind implements Event.
func (MessageRejected) Kind() EventKind { return EventKindMessageRejected }

// Class implements Event.
func (MessageRejected) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageRejected) MarshalJSON() ([]byte, error) {
	type plain MessageRejected
	return marshalKind(EventKindMessageRejected, plain(e))
}

// Kind implements Event.
func (AbortRequested) Kind() EventKind { return EventKindAbortRequested }

// Class implements Event.
func (AbortRequested) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e AbortRequested) MarshalJSON() ([]byte, error) {
	type plain AbortRequested
	return marshalKind(EventKindAbortRequested, plain(e))
}

// Kind implements Event.
func (SessionClaimed) Kind() EventKind { return EventKindSessionClaimed }

// Class implements Event.
func (SessionClaimed) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionClaimed) MarshalJSON() ([]byte, error) {
	type plain SessionClaimed
	return marshalKind(EventKindSessionClaimed, plain(e))
}

// Kind implements Event.
func (SessionReleased) Kind() EventKind { return EventKindSessionReleased }

// Class implements Event.
func (SessionReleased) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionReleased) MarshalJSON() ([]byte, error) {
	type plain SessionReleased
	return marshalKind(EventKindSessionReleased, plain(e))
}

// Kind implements Event.
func (SessionFenced) Kind() EventKind { return EventKindSessionFenced }

// Class implements Event.
func (SessionFenced) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionFenced) MarshalJSON() ([]byte, error) {
	type plain SessionFenced
	return marshalKind(EventKindSessionFenced, plain(e))
}

// Kind implements Event.
func (LivenessLapsed) Kind() EventKind { return EventKindLivenessLapsed }

// Class implements Event.
func (LivenessLapsed) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e LivenessLapsed) MarshalJSON() ([]byte, error) {
	type plain LivenessLapsed
	return marshalKind(EventKindLivenessLapsed, plain(e))
}

// Kind implements Event.
func (SessionCreated) Kind() EventKind { return EventKindSessionCreated }

// Class implements Event.
func (SessionCreated) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionCreated) MarshalJSON() ([]byte, error) {
	type plain SessionCreated
	return marshalKind(EventKindSessionCreated, plain(e))
}

// Kind implements Event.
func (MetadataChanged) Kind() EventKind { return EventKindMetadataChanged }

// Class implements Event.
func (MetadataChanged) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MetadataChanged) MarshalJSON() ([]byte, error) {
	type plain MetadataChanged
	return marshalKind(EventKindMetadataChanged, plain(e))
}

// Kind implements Event.
func (CompactionStarted) Kind() EventKind { return EventKindCompactionStarted }

// Class implements Event.
func (CompactionStarted) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionStarted) MarshalJSON() ([]byte, error) {
	type plain CompactionStarted
	return marshalKind(EventKindCompactionStarted, plain(e))
}

// Kind implements Event.
func (CompactionEnded) Kind() EventKind { return EventKindCompactionEnded }

// Class implements Event.
func (CompactionEnded) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionEnded) MarshalJSON() ([]byte, error) {
	type plain CompactionEnded
	return marshalKind(EventKindCompactionEnded, plain(e))
}

// Kind implements Event.
func (CompactionFailed) Kind() EventKind { return EventKindCompactionFailed }

// Class implements Event.
func (CompactionFailed) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionFailed) MarshalJSON() ([]byte, error) {
	type plain CompactionFailed
	return marshalKind(EventKindCompactionFailed, plain(e))
}

// Kind implements Event.
func (SegmentClosed) Kind() EventKind { return EventKindSegmentClosed }

// Class implements Event.
func (SegmentClosed) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SegmentClosed) MarshalJSON() ([]byte, error) {
	type plain SegmentClosed
	return marshalKind(EventKindSegmentClosed, plain(e))
}

// Kind implements Event.
func (SessionUnloaded) Kind() EventKind { return EventKindSessionUnloaded }

// Class implements Event.
func (SessionUnloaded) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionUnloaded) MarshalJSON() ([]byte, error) {
	type plain SessionUnloaded
	return marshalKind(EventKindSessionUnloaded, plain(e))
}

// Kind implements Event.
func (SessionDeleted) Kind() EventKind { return EventKindSessionDeleted }

// Class implements Event.
func (SessionDeleted) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionDeleted) MarshalJSON() ([]byte, error) {
	type plain SessionDeleted
	return marshalKind(EventKindSessionDeleted, plain(e))
}

// Kind implements Event.
func (SessionLoaded) Kind() EventKind { return EventKindSessionLoaded }

// Class implements Event.
func (SessionLoaded) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionLoaded) MarshalJSON() ([]byte, error) {
	type plain SessionLoaded
	return marshalKind(EventKindSessionLoaded, plain(e))
}

// Kind implements Event.
func (SessionSaved) Kind() EventKind { return EventKindSessionSaved }

// Class implements Event.
func (SessionSaved) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionSaved) MarshalJSON() ([]byte, error) {
	type plain SessionSaved
	return marshalKind(EventKindSessionSaved, plain(e))
}

// Kind implements Event.
func (StoreWriteFailed) Kind() EventKind { return EventKindStoreWriteFailed }

// Class implements Event.
func (StoreWriteFailed) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e StoreWriteFailed) MarshalJSON() ([]byte, error) {
	type plain StoreWriteFailed
	return marshalKind(EventKindStoreWriteFailed, plain(e))
}

// Kind implements Event.
func (ExtensionFailed) Kind() EventKind { return EventKindExtensionFailed }

// Class implements Event.
func (ExtensionFailed) Class() EventClass { return EventClassStored }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ExtensionFailed) MarshalJSON() ([]byte, error) {
	type plain ExtensionFailed
	return marshalKind(EventKindExtensionFailed, plain(e))
}

// Kind implements Event.
func (EventsSkipped) Kind() EventKind { return EventKindEventsSkipped }

// Class implements Event.
func (EventsSkipped) Class() EventClass { return EventClassInstance }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e EventsSkipped) MarshalJSON() ([]byte, error) {
	type plain EventsSkipped
	return marshalKind(EventKindEventsSkipped, plain(e))
}

// eventDecoders turns the JSON of each kind back into its type.
var eventDecoders = map[EventKind]func([]byte) (Event, error){
	EventKindRunStarted:          decodeAs[RunStarted],
	EventKindRunEnded:            decodeAs[RunEnded],
	EventKindTurnStarted:         decodeAs[TurnStarted],
	EventKindTurnEnded:           decodeAs[TurnEnded],
	EventKindContextBuilt:        decodeAs[ContextBuilt],
	EventKindReplyStarted:        decodeAs[ReplyStarted],
	EventKindReplyEnded:          decodeAs[ReplyEnded],
	EventKindTextDelta:           decodeAs[TextDelta],
	EventKindReasoningDelta:      decodeAs[ReasoningDelta],
	EventKindReplyCutOff:         decodeAs[ReplyCutOff],
	EventKindReplyPartKept:       decodeAs[ReplyPartKept],
	EventKindModelRetried:        decodeAs[ModelRetried],
	EventKindToolStarted:         decodeAs[ToolStarted],
	EventKindToolEnded:           decodeAs[ToolEnded],
	EventKindToolProgress:        decodeAs[ToolProgress],
	EventKindToolResultCommitted: decodeAs[ToolResultCommitted],
	EventKindToolResultSpilled:   decodeAs[ToolResultSpilled],
	EventKindToolLoaded:          decodeAs[ToolLoaded],
	EventKindToolUnloaded:        decodeAs[ToolUnloaded],
	EventKindRunRequested:        decodeAs[RunRequested],
	EventKindRunWaiting:          decodeAs[RunWaiting],
	EventKindRunRejected:         decodeAs[RunRejected],
	EventKindMessageQueued:       decodeAs[MessageQueued],
	EventKindMessageCommitted:    decodeAs[MessageCommitted],
	EventKindMessageDropped:      decodeAs[MessageDropped],
	EventKindNoticeQueued:        decodeAs[NoticeQueued],
	EventKindNoticeCommitted:     decodeAs[NoticeCommitted],
	EventKindMessageRejected:     decodeAs[MessageRejected],
	EventKindAbortRequested:      decodeAs[AbortRequested],
	EventKindSessionClaimed:      decodeAs[SessionClaimed],
	EventKindSessionReleased:     decodeAs[SessionReleased],
	EventKindSessionFenced:       decodeAs[SessionFenced],
	EventKindLivenessLapsed:      decodeAs[LivenessLapsed],
	EventKindSessionCreated:      decodeAs[SessionCreated],
	EventKindMetadataChanged:     decodeAs[MetadataChanged],
	EventKindCompactionStarted:   decodeAs[CompactionStarted],
	EventKindCompactionEnded:     decodeAs[CompactionEnded],
	EventKindCompactionFailed:    decodeAs[CompactionFailed],
	EventKindSegmentClosed:       decodeAs[SegmentClosed],
	EventKindSessionUnloaded:     decodeAs[SessionUnloaded],
	EventKindSessionDeleted:      decodeAs[SessionDeleted],
	EventKindSessionLoaded:       decodeAs[SessionLoaded],
	EventKindSessionSaved:        decodeAs[SessionSaved],
	EventKindStoreWriteFailed:    decodeAs[StoreWriteFailed],
	EventKindExtensionFailed:     decodeAs[ExtensionFailed],
	EventKindEventsSkipped:       decodeAs[EventsSkipped],
}
