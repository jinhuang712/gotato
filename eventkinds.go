package gotato

// The kind, the class and the JSON codec of every Event type. The fields of each
// type are in event.go.

// The kinds of Event, as they are named in JSON.
const (
	KindRunStarted          EventKind = "run_started"
	KindRunEnded            EventKind = "run_ended"
	KindTurnStarted         EventKind = "turn_started"
	KindTurnEnded           EventKind = "turn_ended"
	KindContextBuilt        EventKind = "context_built"
	KindReplyStarted        EventKind = "reply_started"
	KindReplyEnded          EventKind = "reply_ended"
	KindTextDelta           EventKind = "text_delta"
	KindReasoningDelta      EventKind = "reasoning_delta"
	KindReplyCutOff         EventKind = "reply_cut_off"
	KindReplyPartKept       EventKind = "reply_part_kept"
	KindModelRetried        EventKind = "model_retried"
	KindToolStarted         EventKind = "tool_started"
	KindToolEnded           EventKind = "tool_ended"
	KindToolProgress        EventKind = "tool_progress"
	KindToolResultCommitted EventKind = "tool_result_committed"
	KindToolResultSpilled   EventKind = "tool_result_spilled"
	KindToolLoaded          EventKind = "tool_loaded"
	KindToolUnloaded        EventKind = "tool_unloaded"
	KindRunRequested        EventKind = "run_requested"
	KindRunWaiting          EventKind = "run_waiting"
	KindRunRejected         EventKind = "run_rejected"
	KindMessageQueued       EventKind = "message_queued"
	KindMessageCommitted    EventKind = "message_committed"
	KindMessageDropped      EventKind = "message_dropped"
	KindMessageWithdrawn    EventKind = "message_withdrawn"
	KindMessageRejected     EventKind = "message_rejected"
	KindAbortRequested      EventKind = "abort_requested"
	KindSessionClaimed      EventKind = "session_claimed"
	KindSessionReleased     EventKind = "session_released"
	KindSessionFenced       EventKind = "session_fenced"
	KindLivenessLapsed      EventKind = "liveness_lapsed"
	KindSessionCreated      EventKind = "session_created"
	KindMetadataChanged     EventKind = "metadata_changed"
	KindCompactionStarted   EventKind = "compaction_started"
	KindCompactionEnded     EventKind = "compaction_ended"
	KindCompactionFailed    EventKind = "compaction_failed"
	KindSegmentClosed       EventKind = "segment_closed"
	KindSessionUnloaded     EventKind = "session_unloaded"
	KindSessionDeleted      EventKind = "session_deleted"
	KindSessionLoaded       EventKind = "session_loaded"
	KindSessionSaved        EventKind = "session_saved"
	KindStoreWriteFailed    EventKind = "store_write_failed"
	KindExtensionFailed     EventKind = "extension_failed"
	KindEventsSkipped       EventKind = "events_skipped"
)

var eventClasses = map[EventKind]EventClass{
	KindRunStarted:          ClassStored,
	KindRunEnded:            ClassStored,
	KindTurnStarted:         ClassStored,
	KindTurnEnded:           ClassStored,
	KindContextBuilt:        ClassStored,
	KindReplyStarted:        ClassStored,
	KindReplyEnded:          ClassStored,
	KindTextDelta:           ClassDelta,
	KindReasoningDelta:      ClassDelta,
	KindReplyCutOff:         ClassStored,
	KindReplyPartKept:       ClassStored,
	KindModelRetried:        ClassStored,
	KindToolStarted:         ClassStored,
	KindToolEnded:           ClassStored,
	KindToolProgress:        ClassDelta,
	KindToolResultCommitted: ClassStored,
	KindToolResultSpilled:   ClassStored,
	KindToolLoaded:          ClassStored,
	KindToolUnloaded:        ClassStored,
	KindRunRequested:        ClassInstance,
	KindRunWaiting:          ClassInstance,
	KindRunRejected:         ClassInstance,
	KindMessageQueued:       ClassStored,
	KindMessageCommitted:    ClassStored,
	KindMessageDropped:      ClassStored,
	KindMessageWithdrawn:    ClassStored,
	KindMessageRejected:     ClassStored,
	KindAbortRequested:      ClassStored,
	KindSessionClaimed:      ClassStored,
	KindSessionReleased:     ClassStored,
	KindSessionFenced:       ClassInstance,
	KindLivenessLapsed:      ClassInstance,
	KindSessionCreated:      ClassStored,
	KindMetadataChanged:     ClassStored,
	KindCompactionStarted:   ClassStored,
	KindCompactionEnded:     ClassStored,
	KindCompactionFailed:    ClassStored,
	KindSegmentClosed:       ClassStored,
	KindSessionUnloaded:     ClassInstance,
	KindSessionDeleted:      ClassInstance,
	KindSessionLoaded:       ClassInstance,
	KindSessionSaved:        ClassInstance,
	KindStoreWriteFailed:    ClassInstance,
	KindExtensionFailed:     ClassStored,
	KindEventsSkipped:       ClassInstance,
}

// Kind implements Event.
func (RunStarted) Kind() EventKind { return KindRunStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunStarted) MarshalJSON() ([]byte, error) {
	type plain RunStarted
	return marshalKind(KindRunStarted, plain(e))
}

// Kind implements Event.
func (RunEnded) Kind() EventKind { return KindRunEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunEnded) MarshalJSON() ([]byte, error) {
	type plain RunEnded
	return marshalKind(KindRunEnded, plain(e))
}

// Kind implements Event.
func (TurnStarted) Kind() EventKind { return KindTurnStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TurnStarted) MarshalJSON() ([]byte, error) {
	type plain TurnStarted
	return marshalKind(KindTurnStarted, plain(e))
}

// Kind implements Event.
func (TurnEnded) Kind() EventKind { return KindTurnEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TurnEnded) MarshalJSON() ([]byte, error) {
	type plain TurnEnded
	return marshalKind(KindTurnEnded, plain(e))
}

// Kind implements Event.
func (ContextBuilt) Kind() EventKind { return KindContextBuilt }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ContextBuilt) MarshalJSON() ([]byte, error) {
	type plain ContextBuilt
	return marshalKind(KindContextBuilt, plain(e))
}

// Kind implements Event.
func (ReplyStarted) Kind() EventKind { return KindReplyStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyStarted) MarshalJSON() ([]byte, error) {
	type plain ReplyStarted
	return marshalKind(KindReplyStarted, plain(e))
}

// Kind implements Event.
func (ReplyEnded) Kind() EventKind { return KindReplyEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyEnded) MarshalJSON() ([]byte, error) {
	type plain ReplyEnded
	return marshalKind(KindReplyEnded, plain(e))
}

// Kind implements Event.
func (TextDelta) Kind() EventKind { return KindTextDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e TextDelta) MarshalJSON() ([]byte, error) {
	type plain TextDelta
	return marshalKind(KindTextDelta, plain(e))
}

// Kind implements Event.
func (ReasoningDelta) Kind() EventKind { return KindReasoningDelta }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReasoningDelta) MarshalJSON() ([]byte, error) {
	type plain ReasoningDelta
	return marshalKind(KindReasoningDelta, plain(e))
}

// Kind implements Event.
func (ReplyCutOff) Kind() EventKind { return KindReplyCutOff }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyCutOff) MarshalJSON() ([]byte, error) {
	type plain ReplyCutOff
	return marshalKind(KindReplyCutOff, plain(e))
}

// Kind implements Event.
func (ReplyPartKept) Kind() EventKind { return KindReplyPartKept }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ReplyPartKept) MarshalJSON() ([]byte, error) {
	type plain ReplyPartKept
	return marshalKind(KindReplyPartKept, plain(e))
}

// Kind implements Event.
func (ModelRetried) Kind() EventKind { return KindModelRetried }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ModelRetried) MarshalJSON() ([]byte, error) {
	type plain ModelRetried
	return marshalKind(KindModelRetried, plain(e))
}

// Kind implements Event.
func (ToolStarted) Kind() EventKind { return KindToolStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolStarted) MarshalJSON() ([]byte, error) {
	type plain ToolStarted
	return marshalKind(KindToolStarted, plain(e))
}

// Kind implements Event.
func (ToolEnded) Kind() EventKind { return KindToolEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolEnded) MarshalJSON() ([]byte, error) {
	type plain ToolEnded
	return marshalKind(KindToolEnded, plain(e))
}

// Kind implements Event.
func (ToolProgress) Kind() EventKind { return KindToolProgress }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolProgress) MarshalJSON() ([]byte, error) {
	type plain ToolProgress
	return marshalKind(KindToolProgress, plain(e))
}

// Kind implements Event.
func (ToolResultCommitted) Kind() EventKind { return KindToolResultCommitted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolResultCommitted) MarshalJSON() ([]byte, error) {
	type plain ToolResultCommitted
	return marshalKind(KindToolResultCommitted, plain(e))
}

// Kind implements Event.
func (ToolResultSpilled) Kind() EventKind { return KindToolResultSpilled }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolResultSpilled) MarshalJSON() ([]byte, error) {
	type plain ToolResultSpilled
	return marshalKind(KindToolResultSpilled, plain(e))
}

// Kind implements Event.
func (ToolLoaded) Kind() EventKind { return KindToolLoaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolLoaded) MarshalJSON() ([]byte, error) {
	type plain ToolLoaded
	return marshalKind(KindToolLoaded, plain(e))
}

// Kind implements Event.
func (ToolUnloaded) Kind() EventKind { return KindToolUnloaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ToolUnloaded) MarshalJSON() ([]byte, error) {
	type plain ToolUnloaded
	return marshalKind(KindToolUnloaded, plain(e))
}

// Kind implements Event.
func (RunRequested) Kind() EventKind { return KindRunRequested }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunRequested) MarshalJSON() ([]byte, error) {
	type plain RunRequested
	return marshalKind(KindRunRequested, plain(e))
}

// Kind implements Event.
func (RunWaiting) Kind() EventKind { return KindRunWaiting }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunWaiting) MarshalJSON() ([]byte, error) {
	type plain RunWaiting
	return marshalKind(KindRunWaiting, plain(e))
}

// Kind implements Event.
func (RunRejected) Kind() EventKind { return KindRunRejected }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e RunRejected) MarshalJSON() ([]byte, error) {
	type plain RunRejected
	return marshalKind(KindRunRejected, plain(e))
}

// Kind implements Event.
func (MessageQueued) Kind() EventKind { return KindMessageQueued }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageQueued) MarshalJSON() ([]byte, error) {
	type plain MessageQueued
	return marshalKind(KindMessageQueued, plain(e))
}

// Kind implements Event.
func (MessageCommitted) Kind() EventKind { return KindMessageCommitted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageCommitted) MarshalJSON() ([]byte, error) {
	type plain MessageCommitted
	return marshalKind(KindMessageCommitted, plain(e))
}

// Kind implements Event.
func (MessageDropped) Kind() EventKind { return KindMessageDropped }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageDropped) MarshalJSON() ([]byte, error) {
	type plain MessageDropped
	return marshalKind(KindMessageDropped, plain(e))
}

// Kind implements Event.
func (MessageWithdrawn) Kind() EventKind { return KindMessageWithdrawn }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageWithdrawn) MarshalJSON() ([]byte, error) {
	type plain MessageWithdrawn
	return marshalKind(KindMessageWithdrawn, plain(e))
}

// Kind implements Event.
func (MessageRejected) Kind() EventKind { return KindMessageRejected }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MessageRejected) MarshalJSON() ([]byte, error) {
	type plain MessageRejected
	return marshalKind(KindMessageRejected, plain(e))
}

// Kind implements Event.
func (AbortRequested) Kind() EventKind { return KindAbortRequested }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e AbortRequested) MarshalJSON() ([]byte, error) {
	type plain AbortRequested
	return marshalKind(KindAbortRequested, plain(e))
}

// Kind implements Event.
func (SessionClaimed) Kind() EventKind { return KindSessionClaimed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionClaimed) MarshalJSON() ([]byte, error) {
	type plain SessionClaimed
	return marshalKind(KindSessionClaimed, plain(e))
}

// Kind implements Event.
func (SessionReleased) Kind() EventKind { return KindSessionReleased }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionReleased) MarshalJSON() ([]byte, error) {
	type plain SessionReleased
	return marshalKind(KindSessionReleased, plain(e))
}

// Kind implements Event.
func (SessionFenced) Kind() EventKind { return KindSessionFenced }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionFenced) MarshalJSON() ([]byte, error) {
	type plain SessionFenced
	return marshalKind(KindSessionFenced, plain(e))
}

// Kind implements Event.
func (LivenessLapsed) Kind() EventKind { return KindLivenessLapsed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e LivenessLapsed) MarshalJSON() ([]byte, error) {
	type plain LivenessLapsed
	return marshalKind(KindLivenessLapsed, plain(e))
}

// Kind implements Event.
func (SessionCreated) Kind() EventKind { return KindSessionCreated }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionCreated) MarshalJSON() ([]byte, error) {
	type plain SessionCreated
	return marshalKind(KindSessionCreated, plain(e))
}

// Kind implements Event.
func (MetadataChanged) Kind() EventKind { return KindMetadataChanged }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e MetadataChanged) MarshalJSON() ([]byte, error) {
	type plain MetadataChanged
	return marshalKind(KindMetadataChanged, plain(e))
}

// Kind implements Event.
func (CompactionStarted) Kind() EventKind { return KindCompactionStarted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionStarted) MarshalJSON() ([]byte, error) {
	type plain CompactionStarted
	return marshalKind(KindCompactionStarted, plain(e))
}

// Kind implements Event.
func (CompactionEnded) Kind() EventKind { return KindCompactionEnded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionEnded) MarshalJSON() ([]byte, error) {
	type plain CompactionEnded
	return marshalKind(KindCompactionEnded, plain(e))
}

// Kind implements Event.
func (CompactionFailed) Kind() EventKind { return KindCompactionFailed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e CompactionFailed) MarshalJSON() ([]byte, error) {
	type plain CompactionFailed
	return marshalKind(KindCompactionFailed, plain(e))
}

// Kind implements Event.
func (SegmentClosed) Kind() EventKind { return KindSegmentClosed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SegmentClosed) MarshalJSON() ([]byte, error) {
	type plain SegmentClosed
	return marshalKind(KindSegmentClosed, plain(e))
}

// Kind implements Event.
func (SessionUnloaded) Kind() EventKind { return KindSessionUnloaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionUnloaded) MarshalJSON() ([]byte, error) {
	type plain SessionUnloaded
	return marshalKind(KindSessionUnloaded, plain(e))
}

// Kind implements Event.
func (SessionDeleted) Kind() EventKind { return KindSessionDeleted }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionDeleted) MarshalJSON() ([]byte, error) {
	type plain SessionDeleted
	return marshalKind(KindSessionDeleted, plain(e))
}

// Kind implements Event.
func (SessionLoaded) Kind() EventKind { return KindSessionLoaded }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionLoaded) MarshalJSON() ([]byte, error) {
	type plain SessionLoaded
	return marshalKind(KindSessionLoaded, plain(e))
}

// Kind implements Event.
func (SessionSaved) Kind() EventKind { return KindSessionSaved }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e SessionSaved) MarshalJSON() ([]byte, error) {
	type plain SessionSaved
	return marshalKind(KindSessionSaved, plain(e))
}

// Kind implements Event.
func (StoreWriteFailed) Kind() EventKind { return KindStoreWriteFailed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e StoreWriteFailed) MarshalJSON() ([]byte, error) {
	type plain StoreWriteFailed
	return marshalKind(KindStoreWriteFailed, plain(e))
}

// Kind implements Event.
func (ExtensionFailed) Kind() EventKind { return KindExtensionFailed }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e ExtensionFailed) MarshalJSON() ([]byte, error) {
	type plain ExtensionFailed
	return marshalKind(KindExtensionFailed, plain(e))
}

// Kind implements Event.
func (EventsSkipped) Kind() EventKind { return KindEventsSkipped }

// MarshalJSON writes the Event as one flat object whose kind names its type.
func (e EventsSkipped) MarshalJSON() ([]byte, error) {
	type plain EventsSkipped
	return marshalKind(KindEventsSkipped, plain(e))
}

// eventDecoders turns the JSON of each kind back into its type.
var eventDecoders = map[EventKind]func([]byte) (Event, error){
	KindRunStarted:          decodeAs[RunStarted],
	KindRunEnded:            decodeAs[RunEnded],
	KindTurnStarted:         decodeAs[TurnStarted],
	KindTurnEnded:           decodeAs[TurnEnded],
	KindContextBuilt:        decodeAs[ContextBuilt],
	KindReplyStarted:        decodeAs[ReplyStarted],
	KindReplyEnded:          decodeAs[ReplyEnded],
	KindTextDelta:           decodeAs[TextDelta],
	KindReasoningDelta:      decodeAs[ReasoningDelta],
	KindReplyCutOff:         decodeAs[ReplyCutOff],
	KindReplyPartKept:       decodeAs[ReplyPartKept],
	KindModelRetried:        decodeAs[ModelRetried],
	KindToolStarted:         decodeAs[ToolStarted],
	KindToolEnded:           decodeAs[ToolEnded],
	KindToolProgress:        decodeAs[ToolProgress],
	KindToolResultCommitted: decodeAs[ToolResultCommitted],
	KindToolResultSpilled:   decodeAs[ToolResultSpilled],
	KindToolLoaded:          decodeAs[ToolLoaded],
	KindToolUnloaded:        decodeAs[ToolUnloaded],
	KindRunRequested:        decodeAs[RunRequested],
	KindRunWaiting:          decodeAs[RunWaiting],
	KindRunRejected:         decodeAs[RunRejected],
	KindMessageQueued:       decodeAs[MessageQueued],
	KindMessageCommitted:    decodeAs[MessageCommitted],
	KindMessageDropped:      decodeAs[MessageDropped],
	KindMessageWithdrawn:    decodeAs[MessageWithdrawn],
	KindMessageRejected:     decodeAs[MessageRejected],
	KindAbortRequested:      decodeAs[AbortRequested],
	KindSessionClaimed:      decodeAs[SessionClaimed],
	KindSessionReleased:     decodeAs[SessionReleased],
	KindSessionFenced:       decodeAs[SessionFenced],
	KindLivenessLapsed:      decodeAs[LivenessLapsed],
	KindSessionCreated:      decodeAs[SessionCreated],
	KindMetadataChanged:     decodeAs[MetadataChanged],
	KindCompactionStarted:   decodeAs[CompactionStarted],
	KindCompactionEnded:     decodeAs[CompactionEnded],
	KindCompactionFailed:    decodeAs[CompactionFailed],
	KindSegmentClosed:       decodeAs[SegmentClosed],
	KindSessionUnloaded:     decodeAs[SessionUnloaded],
	KindSessionDeleted:      decodeAs[SessionDeleted],
	KindSessionLoaded:       decodeAs[SessionLoaded],
	KindSessionSaved:        decodeAs[SessionSaved],
	KindStoreWriteFailed:    decodeAs[StoreWriteFailed],
	KindExtensionFailed:     decodeAs[ExtensionFailed],
	KindEventsSkipped:       decodeAs[EventsSkipped],
}
