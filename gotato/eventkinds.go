package gotato

// The kind, the class and the JSON codec of every Event type. The fields of each
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

var eventClasses = map[EventKind]EventClass{
	EventKindRunStarted:          EventClassStored,
	EventKindRunEnded:            EventClassStored,
	EventKindTurnStarted:         EventClassStored,
	EventKindTurnEnded:           EventClassStored,
	EventKindContextBuilt:        EventClassStored,
	EventKindReplyStarted:        EventClassStored,
	EventKindReplyEnded:          EventClassStored,
	EventKindTextDelta:           EventClassDelta,
	EventKindReasoningDelta:      EventClassDelta,
	EventKindReplyCutOff:         EventClassStored,
	EventKindReplyPartKept:       EventClassStored,
	EventKindModelRetried:        EventClassStored,
	EventKindToolStarted:         EventClassStored,
	EventKindToolEnded:           EventClassStored,
	EventKindToolProgress:        EventClassDelta,
	EventKindToolResultCommitted: EventClassStored,
	EventKindToolResultSpilled:   EventClassStored,
	EventKindToolLoaded:          EventClassStored,
	EventKindToolUnloaded:        EventClassStored,
	EventKindRunRequested:        EventClassInstance,
	EventKindRunWaiting:          EventClassInstance,
	EventKindRunRejected:         EventClassInstance,
	EventKindMessageQueued:       EventClassStored,
	EventKindMessageCommitted:    EventClassStored,
	EventKindMessageDropped:      EventClassStored,
	EventKindNoticeQueued:        EventClassStored,
	EventKindNoticeCommitted:     EventClassStored,
	EventKindMessageRejected:     EventClassStored,
	EventKindAbortRequested:      EventClassStored,
	EventKindSessionClaimed:      EventClassStored,
	EventKindSessionReleased:     EventClassStored,
	EventKindSessionFenced:       EventClassInstance,
	EventKindLivenessLapsed:      EventClassInstance,
	EventKindSessionCreated:      EventClassStored,
	EventKindMetadataChanged:     EventClassStored,
	EventKindCompactionStarted:   EventClassStored,
	EventKindCompactionEnded:     EventClassStored,
	EventKindCompactionFailed:    EventClassStored,
	EventKindSegmentClosed:       EventClassStored,
	EventKindSessionUnloaded:     EventClassInstance,
	EventKindSessionDeleted:      EventClassInstance,
	EventKindSessionLoaded:       EventClassInstance,
	EventKindSessionSaved:        EventClassInstance,
	EventKindStoreWriteFailed:    EventClassInstance,
	EventKindExtensionFailed:     EventClassStored,
	EventKindEventsSkipped:       EventClassInstance,
}

// Kind implements Event.
func (RunStarted) Kind() EventKind { return EventKindRunStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunStarted) MarshalJSON() ([]byte, error) {
	type plain RunStarted
	return marshalKind(EventKindRunStarted, plain(e))
}

// Kind implements Event.
func (RunEnded) Kind() EventKind { return EventKindRunEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunEnded) MarshalJSON() ([]byte, error) {
	type plain RunEnded
	return marshalKind(EventKindRunEnded, plain(e))
}

// Kind implements Event.
func (TurnStarted) Kind() EventKind { return EventKindTurnStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TurnStarted) MarshalJSON() ([]byte, error) {
	type plain TurnStarted
	return marshalKind(EventKindTurnStarted, plain(e))
}

// Kind implements Event.
func (TurnEnded) Kind() EventKind { return EventKindTurnEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TurnEnded) MarshalJSON() ([]byte, error) {
	type plain TurnEnded
	return marshalKind(EventKindTurnEnded, plain(e))
}

// Kind implements Event.
func (ContextBuilt) Kind() EventKind { return EventKindContextBuilt }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ContextBuilt) MarshalJSON() ([]byte, error) {
	type plain ContextBuilt
	return marshalKind(EventKindContextBuilt, plain(e))
}

// Kind implements Event.
func (ReplyStarted) Kind() EventKind { return EventKindReplyStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyStarted) MarshalJSON() ([]byte, error) {
	type plain ReplyStarted
	return marshalKind(EventKindReplyStarted, plain(e))
}

// Kind implements Event.
func (ReplyEnded) Kind() EventKind { return EventKindReplyEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyEnded) MarshalJSON() ([]byte, error) {
	type plain ReplyEnded
	return marshalKind(EventKindReplyEnded, plain(e))
}

// Kind implements Event.
func (TextDelta) Kind() EventKind { return EventKindTextDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TextDelta) MarshalJSON() ([]byte, error) {
	type plain TextDelta
	return marshalKind(EventKindTextDelta, plain(e))
}

// Kind implements Event.
func (ReasoningDelta) Kind() EventKind { return EventKindReasoningDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReasoningDelta) MarshalJSON() ([]byte, error) {
	type plain ReasoningDelta
	return marshalKind(EventKindReasoningDelta, plain(e))
}

// Kind implements Event.
func (ReplyCutOff) Kind() EventKind { return EventKindReplyCutOff }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyCutOff) MarshalJSON() ([]byte, error) {
	type plain ReplyCutOff
	return marshalKind(EventKindReplyCutOff, plain(e))
}

// Kind implements Event.
func (ReplyPartKept) Kind() EventKind { return EventKindReplyPartKept }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyPartKept) MarshalJSON() ([]byte, error) {
	type plain ReplyPartKept
	return marshalKind(EventKindReplyPartKept, plain(e))
}

// Kind implements Event.
func (ModelRetried) Kind() EventKind { return EventKindModelRetried }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ModelRetried) MarshalJSON() ([]byte, error) {
	type plain ModelRetried
	return marshalKind(EventKindModelRetried, plain(e))
}

// Kind implements Event.
func (ToolStarted) Kind() EventKind { return EventKindToolStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolStarted) MarshalJSON() ([]byte, error) {
	type plain ToolStarted
	return marshalKind(EventKindToolStarted, plain(e))
}

// Kind implements Event.
func (ToolEnded) Kind() EventKind { return EventKindToolEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolEnded) MarshalJSON() ([]byte, error) {
	type plain ToolEnded
	return marshalKind(EventKindToolEnded, plain(e))
}

// Kind implements Event.
func (ToolProgress) Kind() EventKind { return EventKindToolProgress }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolProgress) MarshalJSON() ([]byte, error) {
	type plain ToolProgress
	return marshalKind(EventKindToolProgress, plain(e))
}

// Kind implements Event.
func (ToolResultCommitted) Kind() EventKind { return EventKindToolResultCommitted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolResultCommitted) MarshalJSON() ([]byte, error) {
	type plain ToolResultCommitted
	return marshalKind(EventKindToolResultCommitted, plain(e))
}

// Kind implements Event.
func (ToolResultSpilled) Kind() EventKind { return EventKindToolResultSpilled }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolResultSpilled) MarshalJSON() ([]byte, error) {
	type plain ToolResultSpilled
	return marshalKind(EventKindToolResultSpilled, plain(e))
}

// Kind implements Event.
func (ToolLoaded) Kind() EventKind { return EventKindToolLoaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolLoaded) MarshalJSON() ([]byte, error) {
	type plain ToolLoaded
	return marshalKind(EventKindToolLoaded, plain(e))
}

// Kind implements Event.
func (ToolUnloaded) Kind() EventKind { return EventKindToolUnloaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolUnloaded) MarshalJSON() ([]byte, error) {
	type plain ToolUnloaded
	return marshalKind(EventKindToolUnloaded, plain(e))
}

// Kind implements Event.
func (RunRequested) Kind() EventKind { return EventKindRunRequested }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunRequested) MarshalJSON() ([]byte, error) {
	type plain RunRequested
	return marshalKind(EventKindRunRequested, plain(e))
}

// Kind implements Event.
func (RunWaiting) Kind() EventKind { return EventKindRunWaiting }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunWaiting) MarshalJSON() ([]byte, error) {
	type plain RunWaiting
	return marshalKind(EventKindRunWaiting, plain(e))
}

// Kind implements Event.
func (RunRejected) Kind() EventKind { return EventKindRunRejected }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunRejected) MarshalJSON() ([]byte, error) {
	type plain RunRejected
	return marshalKind(EventKindRunRejected, plain(e))
}

// Kind implements Event.
func (MessageQueued) Kind() EventKind { return EventKindMessageQueued }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageQueued) MarshalJSON() ([]byte, error) {
	type plain MessageQueued
	return marshalKind(EventKindMessageQueued, plain(e))
}

// Kind implements Event.
func (MessageCommitted) Kind() EventKind { return EventKindMessageCommitted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageCommitted) MarshalJSON() ([]byte, error) {
	type plain MessageCommitted
	return marshalKind(EventKindMessageCommitted, plain(e))
}

// Kind implements Event.
func (MessageDropped) Kind() EventKind { return EventKindMessageDropped }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageDropped) MarshalJSON() ([]byte, error) {
	type plain MessageDropped
	return marshalKind(EventKindMessageDropped, plain(e))
}

// Kind implements Event.
func (NoticeQueued) Kind() EventKind { return EventKindNoticeQueued }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e NoticeQueued) MarshalJSON() ([]byte, error) {
	type plain NoticeQueued
	return marshalKind(EventKindNoticeQueued, plain(e))
}

// Kind implements Event.
func (NoticeCommitted) Kind() EventKind { return EventKindNoticeCommitted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e NoticeCommitted) MarshalJSON() ([]byte, error) {
	type plain NoticeCommitted
	return marshalKind(EventKindNoticeCommitted, plain(e))
}

// Kind implements Event.
func (MessageRejected) Kind() EventKind { return EventKindMessageRejected }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageRejected) MarshalJSON() ([]byte, error) {
	type plain MessageRejected
	return marshalKind(EventKindMessageRejected, plain(e))
}

// Kind implements Event.
func (AbortRequested) Kind() EventKind { return EventKindAbortRequested }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e AbortRequested) MarshalJSON() ([]byte, error) {
	type plain AbortRequested
	return marshalKind(EventKindAbortRequested, plain(e))
}

// Kind implements Event.
func (SessionClaimed) Kind() EventKind { return EventKindSessionClaimed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionClaimed) MarshalJSON() ([]byte, error) {
	type plain SessionClaimed
	return marshalKind(EventKindSessionClaimed, plain(e))
}

// Kind implements Event.
func (SessionReleased) Kind() EventKind { return EventKindSessionReleased }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionReleased) MarshalJSON() ([]byte, error) {
	type plain SessionReleased
	return marshalKind(EventKindSessionReleased, plain(e))
}

// Kind implements Event.
func (SessionFenced) Kind() EventKind { return EventKindSessionFenced }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionFenced) MarshalJSON() ([]byte, error) {
	type plain SessionFenced
	return marshalKind(EventKindSessionFenced, plain(e))
}

// Kind implements Event.
func (LivenessLapsed) Kind() EventKind { return EventKindLivenessLapsed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e LivenessLapsed) MarshalJSON() ([]byte, error) {
	type plain LivenessLapsed
	return marshalKind(EventKindLivenessLapsed, plain(e))
}

// Kind implements Event.
func (SessionCreated) Kind() EventKind { return EventKindSessionCreated }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionCreated) MarshalJSON() ([]byte, error) {
	type plain SessionCreated
	return marshalKind(EventKindSessionCreated, plain(e))
}

// Kind implements Event.
func (MetadataChanged) Kind() EventKind { return EventKindMetadataChanged }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MetadataChanged) MarshalJSON() ([]byte, error) {
	type plain MetadataChanged
	return marshalKind(EventKindMetadataChanged, plain(e))
}

// Kind implements Event.
func (CompactionStarted) Kind() EventKind { return EventKindCompactionStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionStarted) MarshalJSON() ([]byte, error) {
	type plain CompactionStarted
	return marshalKind(EventKindCompactionStarted, plain(e))
}

// Kind implements Event.
func (CompactionEnded) Kind() EventKind { return EventKindCompactionEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionEnded) MarshalJSON() ([]byte, error) {
	type plain CompactionEnded
	return marshalKind(EventKindCompactionEnded, plain(e))
}

// Kind implements Event.
func (CompactionFailed) Kind() EventKind { return EventKindCompactionFailed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionFailed) MarshalJSON() ([]byte, error) {
	type plain CompactionFailed
	return marshalKind(EventKindCompactionFailed, plain(e))
}

// Kind implements Event.
func (SegmentClosed) Kind() EventKind { return EventKindSegmentClosed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SegmentClosed) MarshalJSON() ([]byte, error) {
	type plain SegmentClosed
	return marshalKind(EventKindSegmentClosed, plain(e))
}

// Kind implements Event.
func (SessionUnloaded) Kind() EventKind { return EventKindSessionUnloaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionUnloaded) MarshalJSON() ([]byte, error) {
	type plain SessionUnloaded
	return marshalKind(EventKindSessionUnloaded, plain(e))
}

// Kind implements Event.
func (SessionDeleted) Kind() EventKind { return EventKindSessionDeleted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionDeleted) MarshalJSON() ([]byte, error) {
	type plain SessionDeleted
	return marshalKind(EventKindSessionDeleted, plain(e))
}

// Kind implements Event.
func (SessionLoaded) Kind() EventKind { return EventKindSessionLoaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionLoaded) MarshalJSON() ([]byte, error) {
	type plain SessionLoaded
	return marshalKind(EventKindSessionLoaded, plain(e))
}

// Kind implements Event.
func (SessionSaved) Kind() EventKind { return EventKindSessionSaved }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionSaved) MarshalJSON() ([]byte, error) {
	type plain SessionSaved
	return marshalKind(EventKindSessionSaved, plain(e))
}

// Kind implements Event.
func (StoreWriteFailed) Kind() EventKind { return EventKindStoreWriteFailed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e StoreWriteFailed) MarshalJSON() ([]byte, error) {
	type plain StoreWriteFailed
	return marshalKind(EventKindStoreWriteFailed, plain(e))
}

// Kind implements Event.
func (ExtensionFailed) Kind() EventKind { return EventKindExtensionFailed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ExtensionFailed) MarshalJSON() ([]byte, error) {
	type plain ExtensionFailed
	return marshalKind(EventKindExtensionFailed, plain(e))
}

// Kind implements Event.
func (EventsSkipped) Kind() EventKind { return EventKindEventsSkipped }

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
