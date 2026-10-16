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
	agent, err := NewSession(WithModel(model))
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

// A reasoning block that streams no deltas still ends with its own
// ModelReasoningDone: an Anthropic redacted_thinking block, a thinking block
// with its display omitted, a Responses reasoning item without a summary. The
// Session signed the last reasoning Part on every Done, so the second block's
// signature overwrote the first block's, and the first block was lost on
// replay. Each Done that finds no unsigned reasoning Part starts a new one.
func TestReasoningDoneWithoutDeltasKeepsEachBlock(t *testing.T) {
	first := []byte(`{"type":"thinking","thinking":"think","signature":"s1"}`)
	second := []byte(`{"type":"redacted_thinking","data":"d2"}`)
	for _, tc := range []struct {
		name   string
		events []ModelEvent
		want   []ContentPart
	}{
		{
			name: "thinking then redacted",
			events: []ModelEvent{
				{Kind: ModelReasoningDelta, Text: "thi"},
				{Kind: ModelReasoningDelta, Text: "nk"},
				{Kind: ModelReasoningDone, ReasoningArtifact: first},
				{Kind: ModelReasoningDone, ReasoningArtifact: second},
				{Kind: ModelTextDelta, Text: "answer"},
				{Kind: ModelDone, StopReason: StopEndTurn},
			},
			want: []ContentPart{
				{Kind: ContentReasoning, Text: "think", Signature: first},
				{Kind: ContentReasoning, Signature: second},
				{Kind: ContentText, Text: "answer"},
			},
		},
		{
			name: "two blocks without deltas",
			events: []ModelEvent{
				{Kind: ModelReasoningDone, ReasoningArtifact: first},
				{Kind: ModelReasoningDone, ReasoningArtifact: second},
				{Kind: ModelTextDelta, Text: "answer"},
				{Kind: ModelDone, StopReason: StopEndTurn},
			},
			want: []ContentPart{
				{Kind: ContentReasoning, Signature: first},
				{Kind: ContentReasoning, Signature: second},
				{Kind: ContentText, Text: "answer"},
			},
		},
		{
			name: "signed block after text",
			events: []ModelEvent{
				{Kind: ModelReasoningDelta, Text: "think"},
				{Kind: ModelReasoningDone, ReasoningArtifact: first},
				{Kind: ModelTextDelta, Text: "answer"},
				{Kind: ModelReasoningDone, ReasoningArtifact: second},
				{Kind: ModelDone, StopReason: StopEndTurn},
			},
			want: []ContentPart{
				{Kind: ContentReasoning, Text: "think", Signature: first},
				{Kind: ContentText, Text: "answer"},
				{Kind: ContentReasoning, Signature: second},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &testModel{scripts: [][]ModelEvent{tc.events}}
			agent, err := NewSession(WithModel(model))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = agent.Close(context.Background()) }()
			result, err := agent.Prompt(context.Background(), UserMessage("go"))
			if err != nil {
				t.Fatal(err)
			}
			if got := result.FinalMessage.Parts; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parts = %#v\nwant %#v", got, tc.want)
			}
		})
	}
}
