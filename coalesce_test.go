package gotato

import (
	"context"
	"reflect"
	"testing"
)

// Observed: an assistant reply was stored as one Part per streamed delta
// ("I", "'ll", " start", ...), so a Mow Worker transcript of 113 messages
// took 827 KB, and the Responses gateway replayed every fragment as its own
// output message. The Message holds one Part per contiguous block of one
// kind, and a Reasoning block closes when its signature arrives.
func TestStreamedDeltasCoalesceIntoBlocks(t *testing.T) {
	sig := []byte(`{"id":"rs_1"}`)
	model := &testModel{scripts: [][]ModelEvent{{
		{Kind: ModelReasoningDelta, Text: "think"},
		{Kind: ModelReasoningDelta, Text: " hard"},
		{Kind: ModelReasoningDone, ReasoningArtifact: sig},
		{Kind: ModelReasoningDelta, Text: "second"},
		{Kind: ModelReasoningDelta, Text: " thought"},
		{Kind: ModelTextDelta, Text: "hel"},
		{Kind: ModelTextDelta, Text: "lo"},
		{Kind: ModelTextDelta, Text: " world"},
		{Kind: ModelReasoningDelta, Text: "again"},
		{Kind: ModelTextDelta, Text: "!"},
		{Kind: ModelDone, StopReason: StopEndTurn},
	}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ContentPart{
		{Kind: ContentReasoning, Text: "think hard", Signature: sig},
		{Kind: ContentReasoning, Text: "second thought"},
		{Kind: ContentText, Text: "hello world"},
		{Kind: ContentReasoning, Text: "again"},
		{Kind: ContentText, Text: "!"},
	}
	if got := result.FinalMessage.Parts; !reflect.DeepEqual(got, want) {
		t.Fatalf("parts = %#v\nwant %#v", got, want)
	}
	if result.Metrics.TextBytes != uint64(len("hello world!")) {
		t.Fatalf("text bytes = %d", result.Metrics.TextBytes)
	}
}
