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
		{Kind: ModelEventKindReasoningDelta, Text: "think"},
		{Kind: ModelEventKindReasoningDelta, Text: " hard"},
		{Kind: ModelEventKindReasoningDone, ReasoningArtifact: sig},
		{Kind: ModelEventKindReasoningDelta, Text: "second"},
		{Kind: ModelEventKindReasoningDelta, Text: " thought"},
		{Kind: ModelEventKindTextDelta, Text: "hel"},
		{Kind: ModelEventKindTextDelta, Text: "lo"},
		{Kind: ModelEventKindTextDelta, Text: " world"},
		{Kind: ModelEventKindReasoningDelta, Text: "again"},
		{Kind: ModelEventKindTextDelta, Text: "!"},
		{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
	}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ContentPart{
		{Kind: ContentKindReasoning, Text: "think hard", Signature: sig},
		{Kind: ContentKindReasoning, Text: "second thought"},
		{Kind: ContentKindText, Text: "hello world"},
		{Kind: ContentKindReasoning, Text: "again"},
		{Kind: ContentKindText, Text: "!"},
	}
	if got := result.FinalMessage.Parts; !reflect.DeepEqual(got, want) {
		t.Fatalf("parts = %#v\nwant %#v", got, want)
	}
	if result.Metrics.TextBytes != uint64(len("hello world!")) {
		t.Fatalf("text bytes = %d", result.Metrics.TextBytes)
	}
}

// A reasoning block that streams no deltas still ends with its own
// ModelEventKindReasoningDone: an Anthropic redacted_thinking block, a thinking block
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
				{Kind: ModelEventKindReasoningDelta, Text: "thi"},
				{Kind: ModelEventKindReasoningDelta, Text: "nk"},
				{Kind: ModelEventKindReasoningDone, ReasoningArtifact: first},
				{Kind: ModelEventKindReasoningDone, ReasoningArtifact: second},
				{Kind: ModelEventKindTextDelta, Text: "answer"},
				{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
			},
			want: []ContentPart{
				{Kind: ContentKindReasoning, Text: "think", Signature: first},
				{Kind: ContentKindReasoning, Signature: second},
				{Kind: ContentKindText, Text: "answer"},
			},
		},
		{
			name: "two blocks without deltas",
			events: []ModelEvent{
				{Kind: ModelEventKindReasoningDone, ReasoningArtifact: first},
				{Kind: ModelEventKindReasoningDone, ReasoningArtifact: second},
				{Kind: ModelEventKindTextDelta, Text: "answer"},
				{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
			},
			want: []ContentPart{
				{Kind: ContentKindReasoning, Signature: first},
				{Kind: ContentKindReasoning, Signature: second},
				{Kind: ContentKindText, Text: "answer"},
			},
		},
		{
			name: "signed block after text",
			events: []ModelEvent{
				{Kind: ModelEventKindReasoningDelta, Text: "think"},
				{Kind: ModelEventKindReasoningDone, ReasoningArtifact: first},
				{Kind: ModelEventKindTextDelta, Text: "answer"},
				{Kind: ModelEventKindReasoningDone, ReasoningArtifact: second},
				{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
			},
			want: []ContentPart{
				{Kind: ContentKindReasoning, Text: "think", Signature: first},
				{Kind: ContentKindText, Text: "answer"},
				{Kind: ContentKindReasoning, Signature: second},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &testModel{scripts: [][]ModelEvent{tc.events}}
			agent, err := NewSession(context.Background(), WithModel(model))
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
