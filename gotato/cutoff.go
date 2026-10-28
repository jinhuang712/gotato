package gotato

// cutOffReply is what a Turn keeps of a reply whose stream broke: the text
// received so far, marked StopReasonCutOff, or no Message when no text arrived.
// Reasoning and Tool calls are left out: a provider takes reasoning back only
// whole and signed, and a Tool call that is never answered cannot be sent.
func cutOffReply(partial Message) Message {
	parts := make([]ContentPart, 0, len(partial.Parts))
	for _, part := range partial.Parts {
		if part.Kind == ContentKindText && part.Text != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return Message{}
	}
	return Message{ID: partial.ID, RunID: partial.RunID, Role: RoleAssistant, Parts: coalesceDeltas(parts), StopReason: StopReasonCutOff, Model: partial.Model}
}
