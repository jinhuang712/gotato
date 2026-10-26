package gotato

import "testing"

// TextOf is what a message says: its text Parts. Reasoning is the model's
// thinking and must not leak into a final answer, a summary, or a replayed
// content field; ReasoningOf returns it separately.
func TestTextOfExcludesReasoning(t *testing.T) {
	m := Message{Role: RoleAssistant, Parts: []ContentPart{
		{Kind: ContentReasoning, Text: "let me think"},
		{Kind: ContentText, Text: "the answer"},
		{Kind: ContentReasoning, Text: " again"},
	}}
	if got := TextOf(m); got != "the answer" {
		t.Fatalf("TextOf = %q", got)
	}
	if got := ReasoningOf(m); got != "let me think again" {
		t.Fatalf("ReasoningOf = %q", got)
	}
}
